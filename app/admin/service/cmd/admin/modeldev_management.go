package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bConfig "go-wind-admin/pkg/localdeps/kratos-bootstrap/config"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func runModelDevManagement(ctx context.Context, command string, args []string, stdout io.Writer) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("conf", "", "existing Governance configuration directory")
	tokenPath := flags.String("token-file", "", "private current user access token")
	requestPath := flags.String("request-file", "", "private fixed operation request")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *directory == "" || *tokenPath == "" || *requestPath == "" {
		return errors.New("modeldev management requires --conf DIR --token-file PATH --request-file PATH")
	}
	tokenRaw, err := readModelDevPrivateFile(*tokenPath, 16<<10)
	if err != nil {
		return errors.New("modeldev management requires a nonempty regular token file with mode 0600")
	}
	token := strings.TrimSpace(string(tokenRaw))
	if token == "" {
		return errors.New("modeldev management requires an access token")
	}
	request, err := readModelDevPrivateFile(*requestPath, 3<<20)
	if err != nil {
		return errors.New("modeldev management requires a nonempty regular request file with mode 0600")
	}
	provider, err := bConfig.NewConfigProvider(*directory)
	if err != nil {
		return errors.New("modeldev management configuration unavailable")
	}
	defer func() {
		if err := provider.Close(); err != nil && result == nil {
			result = errors.New("modeldev management configuration cleanup failed")
		}
	}()
	if provider.Load() != nil {
		return errors.New("modeldev management configuration unavailable")
	}
	var cfg conf.Bootstrap
	if provider.Scan(&cfg) != nil {
		return errors.New("modeldev management configuration unavailable")
	}
	if cfg.GetData().GetDatabase().GetDriver() != "postgres" || cfg.GetData().GetDatabase().GetSource() == "" || cfg.GetData().GetDatabase().GetMigrate() || cfg.GetData().GetRedis().GetAddr() == "" || cfg.GetAuthn().GetType() != "jwt" || cfg.GetAuthn().GetJwt() == nil {
		return errors.New("modeldev management requires PostgreSQL without automatic migration, Redis and JWT configuration")
	}
	bctx := bootstrap.NewContextWithParam(ctx, nil, &cfg, bLogger.NopLogger())
	db, closeDB, err := data.NewEntClient(bctx)
	if err != nil {
		return errors.New("modeldev management database unavailable")
	}
	defer closeDB()
	if db.DB().PingContext(ctx) != nil {
		return errors.New("modeldev management database unavailable")
	}
	rdb, closeRedis, err := data.NewRedisClient(bctx)
	if err != nil {
		return errors.New("modeldev management session store unavailable")
	}
	defer closeRedis()
	if rdb.Ping(ctx).Err() != nil {
		return errors.New("modeldev management session store unavailable")
	}
	authenticator, err := newModelDevPauseAuthenticator(bctx, data.NewUserTokenCache(bctx, rdb))
	if err != nil {
		return errors.New("modeldev management JWT configuration invalid")
	}
	verified, err := authenticator.Authenticate(ctx, &authv1.ValidateTokenRequest{ClientType: authv1.ClientType_admin, TokenCategory: authv1.TokenCategory_ACCESS, Token: token})
	if err != nil || verified == nil || !verified.IsValid || verified.Payload.GetUserId() == 0 || verified.Payload.GetTenantId() == 0 {
		return errors.New("modeldev management requires a valid tenant user access token and session")
	}
	principal := &auth.Principal{Type: auth.SubjectUser, ID: verified.Payload.GetUserId(), TenantID: verified.Payload.GetTenantId()}
	ctx = auth.NewPrincipalContext(ctx, principal)
	ctx = viewer.WithContext(ctx, appViewer.NewUserViewer(uint64(principal.ID), uint64(principal.TenantID), 0, "", nil))
	authorization := data.NewModelDevAuthorizationRepo(db)
	if command == "modeldev-inspect" {
		err = authorization.AuthorizeQuery(ctx, principal.TenantID, principal.ID, data.ModelDevGetExecutionPath)
	} else if command == "modeldev-reconcile" || command == "modeldev-cleanup-plan" || command == "modeldev-cleanup-apply" {
		err = authorization.AuthorizeManageExecution(ctx, principal.TenantID, principal.ID)
	} else {
		err = authorization.AuthorizeManageReleaseBinding(ctx, principal.TenantID, principal.ID)
	}
	if err != nil {
		return errors.New("modeldev management forbidden or authorization unavailable")
	}
	tenants := data.NewTenantRepo(bctx, db)
	tenant, err := tenants.ResourceTenantID(ctx, principal.TenantID)
	if err != nil {
		return errors.New("modeldev management tenant mapping unavailable")
	}
	actor, err := principal.Actor()
	if err != nil {
		return errors.New("modeldev management operator unavailable")
	}
	clientConfig, err := data.ModelDevConfigFromEnv()
	if err != nil || clientConfig.Address == "" {
		return errors.New("modeldev management connection unavailable")
	}
	client, closeClient, err := data.NewModelDevClient(clientConfig)
	if err != nil {
		return errors.New("modeldev management connection unavailable")
	}
	defer closeClient()
	scope := data.ModelDevResolveScope{ResourceTenantID: tenant, Actor: actor}
	if command == "modeldev-enable" {
		in, err := decodeModelDevPauseRequest(request)
		if err != nil {
			return err
		}
		change, err := service.NewModelDevBindingService(authorization, tenants, data.NewModelDevReleaseBindingRepo(db), client).Enable(ctx, in)
		if err != nil {
			return err
		}
		if change == nil || change.After == nil {
			return errors.New("modeldev binding result unavailable")
		}
		var before *modelDevPauseBindingView
		if change.Before != nil {
			value := modelDevPauseView(change.Before)
			before = &value
		}
		return json.NewEncoder(stdout).Encode(struct {
			Operator string                    `json:"operator"`
			Before   *modelDevPauseBindingView `json:"before"`
			After    modelDevPauseBindingView  `json:"after"`
			Replayed bool                      `json:"replayed"`
		}{actor, before, modelDevPauseView(change.After), change.Replayed})
	}
	var message proto.Message
	switch command {
	case "modeldev-import-release":
		message = &modeldevv1.ImportReleaseRequest{}
	case "modeldev-import-csv":
		message = &modeldevv1.ImportCSVRequest{}
	case "modeldev-import-finetuning-dataset":
		message = &modeldevv1.ImportFinetuningDatasetRequest{}
	case "modeldev-inspect":
		message = &modeldevv1.InspectExecutionRequest{}
	case "modeldev-reconcile":
		message = &modeldevv1.ReconcileExecutionRequest{}
	case "modeldev-cleanup-plan":
		message = &modeldevv1.PlanExecutionCleanupRequest{}
	case "modeldev-cleanup-apply":
		message = &modeldevv1.ApplyExecutionCleanupRequest{}
	default:
		return errors.New("invalid modeldev management operation")
	}
	if protojson.Unmarshal(request, message) != nil {
		return errors.New("invalid modeldev management request")
	}
	out, err := client.ManagedOperation(ctx, scope, message)
	if err != nil {
		return fmt.Errorf("modeldev management remote operation failed (%s)", status.Code(err))
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(out)
	if err != nil {
		return errors.New("modeldev management result unavailable")
	}
	_, err = stdout.Write(append(raw, '\n'))
	return err
}
