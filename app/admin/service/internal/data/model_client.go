package data

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ModelClientConfig is deployment-only connection material. Public requests
// never select an endpoint, certificate, or server identity.
type ModelClientConfig struct {
	Address, CAFile, CertFile, KeyFile, ServerName string
	Timeout                                        time.Duration
}

// ModelConfigFromEnv reads the explicit Gov-to-Model connection settings.
// Leaving all fields empty disables the BFF and makes startup fail only when a
// model route is actually used.
func ModelConfigFromEnv() (ModelClientConfig, error) {
	c := ModelClientConfig{
		Address:    os.Getenv("ANI_MODEL_ADDR"),
		CAFile:     os.Getenv("ANI_MODEL_CA"),
		CertFile:   os.Getenv("ANI_MODEL_CERT"),
		KeyFile:    os.Getenv("ANI_MODEL_KEY"),
		ServerName: os.Getenv("ANI_MODEL_SERVER_NAME"),
		Timeout:    30 * time.Second,
	}
	if c.Address == "" && c.CAFile == "" && c.CertFile == "" && c.KeyFile == "" && c.ServerName == "" {
		return ModelClientConfig{}, nil
	}
	if c.Address == "" || c.CAFile == "" || c.CertFile == "" || c.KeyFile == "" || c.ServerName == "" || strings.TrimSpace(c.Address) != c.Address || strings.ContainsAny(c.ServerName, "*:/\\ \t\n") {
		return ModelClientConfig{}, errors.New("model client requires address, CA, certificate, key and exact server DNS name")
	}
	if raw := os.Getenv("ANI_MODEL_TIMEOUT"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 || parsed > 45*time.Second {
			return ModelClientConfig{}, errors.New("invalid model client timeout")
		}
		c.Timeout = parsed
	}
	return c, nil
}

type ModelClient struct {
	connection *grpc.ClientConn
	client     modelv1.ModelServiceClient
	timeout    time.Duration
}

func NewModelClient(c ModelClientConfig) (*ModelClient, func(), error) {
	if c.Address == "" || strings.TrimSpace(c.Address) != c.Address || c.ServerName == "" || strings.ContainsAny(c.ServerName, "*:/\\ \t\n") || c.Timeout <= 0 || c.Timeout > 45*time.Second {
		return nil, nil, errors.New("invalid model client configuration")
	}
	certificate, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil || len(certificate.Certificate) == 0 {
		return nil, nil, errors.New("model client certificate unavailable")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || !governanceClientCertificate(leaf) {
		return nil, nil, errors.New("model client requires the registered Governance DNS identity")
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, nil, errors.New("model client CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, nil, errors.New("model client CA contains no certificates")
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		Certificates: []tls.Certificate{certificate},
		ServerName:   c.ServerName,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !hasExactDNSName(state.PeerCertificates[0], c.ServerName) {
				return errors.New("model server exact DNS SAN mismatch")
			}
			return nil
		},
	}
	connection, err := grpc.NewClient(c.Address, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return nil, nil, errors.New("invalid model client address")
	}
	return &ModelClient{connection: connection, client: modelv1.NewModelServiceClient(connection), timeout: c.Timeout}, func() { _ = connection.Close() }, nil
}

func governanceClientCertificate(cert *x509.Certificate) bool {
	return cert != nil && len(cert.DNSNames) == 1 && cert.DNSNames[0] == "ani-governance" &&
		len(cert.URIs) == 0 && len(cert.IPAddresses) == 0 && len(cert.EmailAddresses) == 0
}

func hasExactDNSName(cert *x509.Certificate, name string) bool {
	if cert == nil {
		return false
	}
	matched := false
	for _, dns := range cert.DNSNames {
		if strings.Contains(dns, "*") {
			return false
		}
		matched = matched || dns == name
	}
	return matched
}

func validModelIdentity(tenant, actor string) bool {
	parsed, err := uuid.Parse(tenant)
	if err != nil || parsed == uuid.Nil || parsed.String() != tenant {
		return false
	}
	for _, prefix := range []string{"governance:user:", "governance:access-key:"} {
		if strings.HasPrefix(actor, prefix) {
			id, err := strconv.ParseUint(strings.TrimPrefix(actor, prefix), 10, 32)
			return err == nil && id > 0 && actor == prefix+strconv.FormatUint(id, 10)
		}
	}
	return false
}

func (c *ModelClient) trustedContext(ctx context.Context, tenant, actor string) (context.Context, context.CancelFunc, error) {
	if c == nil || c.client == nil || c.timeout <= 0 {
		return nil, nil, status.Error(codes.Unavailable, "MODEL_ADAPTER_UNAVAILABLE")
	}
	if !validModelIdentity(tenant, actor) {
		return nil, nil, status.Error(codes.Unauthenticated, "invalid trusted Model identity")
	}
	call, cancel := context.WithTimeout(ctx, c.timeout)
	// Replace, rather than append to, inbound metadata. A public caller must
	// never be able to forward a forged tenant, actor, bearer token or operator.
	call = metadata.NewOutgoingContext(call, metadata.Pairs(
		"x-ani-tenant-id", tenant,
		"x-ani-actor", actor,
		"x-ani-request-id", uuid.NewString(),
	))
	return call, cancel, nil
}

func cloneModelRequest[T proto.Message](request T) (T, error) {
	if any(request) == nil || !request.ProtoReflect().IsValid() {
		var zero T
		return zero, status.Error(codes.InvalidArgument, "MODEL_REQUEST_REQUIRED")
	}
	return proto.Clone(request).(T), nil
}

func (c *ModelClient) CreateModel(ctx context.Context, tenant, actor string, request *modelv1.CreateModelRequest) (*modelv1.Model, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.CreateModel(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) GetModel(ctx context.Context, tenant, actor string, request *modelv1.GetModelRequest) (*modelv1.Model, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.GetModel(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) ListModels(ctx context.Context, tenant, actor string, request *modelv1.ListModelsRequest) (*modelv1.ListModelsResponse, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.ListModels(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) SearchExternalModels(ctx context.Context, tenant, actor string, request *modelv1.SearchExternalModelsRequest) (*modelv1.SearchExternalModelsResponse, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.SearchExternalModels(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) DeleteModel(ctx context.Context, tenant, actor string, request *modelv1.DeleteModelRequest) (*emptypb.Empty, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.DeleteModel(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) CreateModelVersion(ctx context.Context, tenant, actor string, request *modelv1.CreateModelVersionRequest) (*modelv1.ModelVersion, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.CreateModelVersion(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) GetModelVersion(ctx context.Context, tenant, actor string, request *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.GetModelVersion(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) ListModelVersions(ctx context.Context, tenant, actor string, request *modelv1.ListModelVersionsRequest) (*modelv1.ListModelVersionsResponse, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.ListModelVersions(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) DeleteModelVersion(ctx context.Context, tenant, actor string, request *modelv1.DeleteModelVersionRequest) (*modelv1.DeleteModelVersionResponse, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.DeleteModelVersion(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) GetUploadURL(ctx context.Context, tenant, actor string, request *modelv1.GetUploadURLRequest) (*modelv1.GetUploadURLResponse, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.GetUploadURL(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) ImportModel(ctx context.Context, tenant, actor string, request *modelv1.ImportModelRequest) (*modelv1.ImportTask, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.ImportModel(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) GetImportTask(ctx context.Context, tenant, actor string, request *modelv1.GetImportTaskRequest) (*modelv1.GetImportTaskResponse, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.GetImportTask(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) RetryImportTask(ctx context.Context, tenant, actor string, request *modelv1.RetryImportTaskRequest) (*modelv1.RetryImportTaskResponse, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.RetryImportTask(call, req, grpc.WaitForReady(true))
}

func (c *ModelClient) GetModelDownloadURL(ctx context.Context, tenant, actor string, request *modelv1.GetModelDownloadURLRequest) (*modelv1.GetModelDownloadURLResponse, error) {
	req, err := cloneModelRequest(request)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.client.GetModelDownloadURL(call, req, grpc.WaitForReady(true))
}
