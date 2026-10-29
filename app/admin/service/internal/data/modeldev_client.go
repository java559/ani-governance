package data

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/pkg/middleware/requestid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Registered service identities for the Governance -> ModelDev internal RPC.
// See docs/interface-integration-register.md (S2S-MODELDEV-CPUP01-01): the
// client presents one exact DNS SAN and verifies the literal ModelDev server
// DNS SAN. Neither is supplied by a public request.
const (
	modelDevClientDNSName = "ani-governance"
	modelDevServerDNSName = "ani-modeldev-service"
)

// ModelDevClientConfig is explicit internal connection material. The server
// identity is fixed to ani-modeldev-service, never supplied by a public request.
type ModelDevClientConfig struct {
	Address, CAFile, CertFile, KeyFile string
	Timeout                            time.Duration
	ModelResolutionTimeout             time.Duration
	ImportTimeout                      time.Duration
}

// ModelDevResolveScope is supplied after current Governance authorization and
// its persisted tenant mapping. It is not a public request DTO.
type ModelDevResolveScope struct {
	ResourceTenantID string
	Actor            string
}

// ModelDevResolution is an unpersisted candidate, not a command receipt.
type ModelDevResolution struct {
	Snapshot          cpup01.Snapshot
	ExecutionSpecHash string
	ResolvedGPU       *acc.ResolvedGpuRequest
}

type ModelDevClient struct {
	connection        *grpc.ClientConn
	client            modeldevv1.ModelDevAdmissionServiceClient
	timeout           time.Duration
	resolutionTimeout time.Duration
	importTimeout     time.Duration
}

func NewModelDevClient(config ModelDevClientConfig) (*ModelDevClient, func(), error) {
	if config.ModelResolutionTimeout == 0 {
		config.ModelResolutionTimeout = 300 * time.Second
	}
	if config.ImportTimeout == 0 {
		config.ImportTimeout = 1800 * time.Second
	}
	if config.Address == "" || strings.TrimSpace(config.Address) != config.Address || config.Timeout <= 0 || config.Timeout > 24*time.Hour || config.ModelResolutionTimeout <= 0 || config.ModelResolutionTimeout > time.Duration(math.MaxUint32)*time.Second || config.ImportTimeout <= 0 || config.ImportTimeout > time.Duration(math.MaxUint32)*time.Second {
		return nil, nil, errors.New("invalid modeldev client configuration")
	}
	security, err := modelDevClientTLS(config)
	if err != nil {
		return nil, nil, err
	}
	connection, err := grpc.NewClient(config.Address, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(security)), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(cpup01.MaxControlRPCBytes), grpc.MaxCallSendMsgSize(cpup01.MaxControlRPCBytes)))
	if err != nil {
		return nil, nil, errors.New("invalid modeldev client configuration")
	}
	client := &ModelDevClient{connection: connection, client: modeldevv1.NewModelDevAdmissionServiceClient(connection), timeout: config.Timeout, resolutionTimeout: config.ModelResolutionTimeout, importTimeout: config.ImportTimeout}
	return client, func() { _ = connection.Close() }, nil
}

func modelDevClientTLS(config ModelDevClientConfig) (*tls.Config, error) {
	unavailable := errors.New("modeldev TLS configuration unavailable")
	pem, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, unavailable
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, unavailable
	}
	certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil || len(certificate.Certificate) == 0 {
		return nil, unavailable
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != modelDevClientDNSName || len(leaf.IPAddresses) != 0 || len(leaf.URIs) != 0 || len(leaf.EmailAddresses) != 0 {
		return nil, unavailable
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, unavailable
	}
	clientUsage := len(leaf.ExtKeyUsage) == 0
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny {
			clientUsage = true
		}
	}
	if !clientUsage {
		return nil, unavailable
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: modelDevServerDNSName,
		// Normal TLS verification checks the chain, usage, time and ServerName.
		// Also require the literal service SAN rather than a wildcard identity.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return unavailable
			}
			matched := false
			for _, name := range state.PeerCertificates[0].DNSNames {
				if strings.Contains(name, "*") {
					return unavailable
				}
				matched = matched || name == modelDevServerDNSName
			}
			if !matched {
				return unavailable
			}
			return nil
		},
	}, nil
}

// ResolvePrepared performs both read-only calls with the same fixed identity,
// accepted_at and official compute observation. No command is sent here.
func (c *ModelDevClient) ResolvePrepared(ctx context.Context, scope ModelDevResolveScope, intent cpup01.Intent, identity cpup01.IdentityRef, selection *compute.ComputeSelection, acceptedAt time.Time) (ModelDevResolution, error) {
	return c.ResolvePreparedWithGPU(ctx, scope, intent, identity, selection, acceptedAt, nil)
}

// ResolvePreparedWithGPU freezes one original ACC response between the two
// ModelDev calls. CPU never calls resolveGPU or creates GPU accounting facts.
func (c *ModelDevClient) ResolvePreparedWithGPU(ctx context.Context, scope ModelDevResolveScope, intent cpup01.Intent, identity cpup01.IdentityRef, selection *compute.ComputeSelection, acceptedAt time.Time, resolveGPU func(context.Context, *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error)) (ModelDevResolution, error) {
	if err := ctx.Err(); err != nil {
		return ModelDevResolution{}, status.FromContextError(err).Err()
	}
	invalid := status.Error(codes.InvalidArgument, "invalid modeldev admission request")
	if c == nil || c.connection == nil || c.client == nil || c.timeout <= 0 {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	if !validModelDevClientActor(scope.Actor) || identity.TenantID != scope.ResourceTenantID || identity.Actor != scope.Actor || acceptedAt.IsZero() || acceptedAt.Nanosecond()%1000 != 0 || compute.ValidateSelection(selection) != nil || selection.PresetId != intent.ComputePresetID || selection.Gpu != nil && resolveGPU == nil {
		return ModelDevResolution{}, invalid
	}
	intentBytes, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil || identity.IntentHash != intentHash {
		return ModelDevResolution{}, invalid
	}
	for _, id := range []string{identity.TenantID, identity.ExecutionID, identity.OperationID} {
		parsed, e := uuid.Parse(id)
		if e != nil || parsed == uuid.Nil || parsed.String() != id {
			return ModelDevResolution{}, invalid
		}
	}
	selectionBytes, err := compute.CanonicalSelection(selection)
	if err != nil {
		return ModelDevResolution{}, invalid
	}
	expectedOrigin, err := contractpb.DecodeComputeSelection(selectionBytes, selection.SelectionDigest)
	if err != nil {
		return ModelDevResolution{}, invalid
	}
	expectedRequest, err := cpup01.GPURequestFor(expectedOrigin)
	if err != nil {
		return ModelDevResolution{}, invalid
	}
	wireIntent, err := contractpb.EncodeIntent(intent)
	if err != nil {
		return ModelDevResolution{}, invalid
	}
	if intent.TimeoutSeconds != nil {
		if *intent.TimeoutSeconds > uint64(math.MaxInt64/int64(time.Second)) {
			return ModelDevResolution{}, invalid
		}
		bounded, cancelDeadline := context.WithDeadline(ctx, acceptedAt.Add(time.Duration(*intent.TimeoutSeconds)*time.Second))
		defer cancelDeadline()
		ctx = bounded
	}
	prepare := &modeldevv1.PrepareAdmissionRequest{Intent: wireIntent, IdentityRef: &modeldevv1.IdentityRef{TenantId: identity.TenantID, Actor: identity.Actor, ExecutionId: identity.ExecutionID, OperationId: identity.OperationID, IntentHash: identity.IntentHash}, AcceptedAt: timestamppb.New(acceptedAt), ComputeSelectionCanonical: selectionBytes, SelectionDigest: selection.SelectionDigest}
	call, cancel, requestID := c.admissionCallBudget(ctx, scope, c.resolutionTimeout)
	preparedReply, err := c.client.PrepareAdmission(call, prepare, grpc.WaitForReady(true))
	callErr := call.Err()
	cancel()
	if err != nil || callErr != nil {
		return ModelDevResolution{}, c.admissionFailure(ctx, callErr, err, requestID)
	}
	if preparedReply == nil || len(preparedReply.ProtoReflect().GetUnknown()) != 0 {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	prepared, err := cpup01.DecodePreparedCandidate(preparedReply.CandidateCanonical, preparedReply.CandidateHash)
	if err != nil {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	returnedIntent, _, err := cpup01.CanonicalIntent(prepared.Intent)
	if err != nil || prepared.Snapshot.GPU != nil || selection.MemoryMib > math.MaxInt64/(1024*1024) || prepared.Snapshot.Resources != (cpup01.CPUResources{Nodes: 1, ProcessesPerNode: 1, RequestMillicpu: selection.CpuMillis, LimitMillicpu: selection.CpuMillis, RequestMemoryBytes: selection.MemoryMib * 1024 * 1024, LimitMemoryBytes: selection.MemoryMib * 1024 * 1024}) || !bytes.Equal(intentBytes, returnedIntent) || prepared.Snapshot.IdentityRef != identity || !prepared.Snapshot.Timing.AcceptedAt.Equal(acceptedAt) || prepared.Snapshot.ComputeOrigin != expectedOrigin || (expectedRequest == nil) != (prepared.GPURequest == nil) || expectedRequest != nil && *prepared.GPURequest != *expectedRequest {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	ctx, cancelExecution := context.WithDeadline(ctx, prepared.Snapshot.Timing.ExecutionDeadlineAt)
	defer cancelExecution()
	if err := ctx.Err(); err != nil {
		return ModelDevResolution{}, status.FromContextError(err).Err()
	}
	finalize := &modeldevv1.FinalizeAdmissionRequest{CandidateCanonical: preparedReply.CandidateCanonical, CandidateHash: preparedReply.CandidateHash}
	var resolvedGPU *acc.ResolvedGpuRequest
	var expectedGPU *cpup01.GPUPlan
	if expectedRequest != nil {
		// Pass an independent copy so an outbound adapter cannot rewrite the
		// verified candidate or select another original request for Finalize.
		request := *prepared.GPURequest
		gpuCall, cancelGPU := context.WithTimeout(ctx, c.timeout)
		resolved, resolveErr := resolveGPU(gpuCall, &request)
		gpuCallErr := gpuCall.Err()
		cancelGPU()
		if ctx.Err() != nil {
			return ModelDevResolution{}, status.FromContextError(ctx.Err()).Err()
		}
		if gpuCallErr != nil {
			return ModelDevResolution{}, status.FromContextError(gpuCallErr).Err()
		}
		if resolveErr != nil {
			return ModelDevResolution{}, resolveErr
		}
		if resolved == nil {
			return ModelDevResolution{}, modelDevAdmissionUnavailable()
		}
		resolvedGPU = proto.Clone(resolved).(*acc.ResolvedGpuRequest)
		original := expectedRequest
		formalRequest := &acc.GpuRequest{ClusterId: original.ClusterID, PoolId: original.PoolID, ProfileId: original.ProfileID, ProfileVersion: original.ProfileVersion, Replicas: original.Replicas, DevicesPerReplica: original.DevicesPerReplica, ContainerName: original.ContainerName}
		if _, err := commandcontract.GpuResolvedQuota(resolvedGPU, formalRequest); err != nil {
			return ModelDevResolution{}, modelDevAdmissionUnavailable()
		}
		finalize.GpuExecutionCanonical, finalize.GpuDisplayCanonical, err = contractpb.EncodeGPUPlanCanonical(resolvedGPU.Execution, resolvedGPU.Selection)
		if err != nil {
			return ModelDevResolution{}, modelDevAdmissionUnavailable()
		}
		expectedGPU, err = contractpb.DecodeGPUPlanCanonical(finalize.GpuExecutionCanonical, finalize.GpuDisplayCanonical)
		if err != nil || expectedGPU.ValidateFor(expectedOrigin) != nil {
			return ModelDevResolution{}, modelDevAdmissionUnavailable()
		}
	}
	call, cancel, requestID = c.admissionCall(ctx, scope)
	reply, err := c.client.FinalizeAdmission(call, finalize, grpc.WaitForReady(true))
	callErr = call.Err()
	cancel()
	if err != nil || callErr != nil {
		return ModelDevResolution{}, c.admissionFailure(ctx, callErr, err, requestID)
	}
	if reply == nil || len(reply.ProtoReflect().GetUnknown()) != 0 {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	snapshot, err := contractpb.DecodeSnapshot(reply.Snapshot)
	if err != nil {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	expected := prepared.Snapshot
	expected.RuntimePlan = snapshot.RuntimePlan
	expected.GPU = expectedGPU
	expectedBytes, err := expected.Canonical()
	if err != nil {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	actualBytes, err := snapshot.Canonical()
	if err != nil || !bytes.Equal(expectedBytes, actualBytes) {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	digest, err := snapshot.Digest()
	if err != nil || digest != reply.ExecutionSpecHash {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	envelope := cpup01.AdmissionEnvelope{TenantID: identity.TenantID, Actor: identity.Actor, OperationID: identity.OperationID, ExecutionID: identity.ExecutionID, Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: digest, AcceptedAt: acceptedAt}
	if _, _, err := envelope.CanonicalPayloads(); err != nil {
		return ModelDevResolution{}, modelDevAdmissionUnavailable()
	}
	return ModelDevResolution{Snapshot: snapshot, ExecutionSpecHash: digest, ResolvedGPU: resolvedGPU}, nil
}

func (c *ModelDevClient) admissionCall(ctx context.Context, scope ModelDevResolveScope) (context.Context, context.CancelFunc, string) {
	return c.admissionCallBudget(ctx, scope, c.timeout)
}

func (c *ModelDevClient) admissionCallBudget(ctx context.Context, scope ModelDevResolveScope, budget time.Duration) (context.Context, context.CancelFunc, string) {
	call, cancel := context.WithTimeout(ctx, budget)
	id := uuid.NewString()
	delegation := metadata.Pairs("x-ani-tenant-id", scope.ResourceTenantID, "x-ani-actor", scope.Actor, "x-ani-request-id", id)
	modelDevAttachParentRequest(ctx, delegation)
	call = metadata.NewOutgoingContext(call, delegation)
	return call, cancel, id
}

func modelDevAttachParentRequest(ctx context.Context, delegation metadata.MD) {
	parent, ok := requestid.FromContext(ctx)
	if !ok || len(parent) > 128 {
		return
	}
	for _, c := range parent {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return
	}
	delegation.Set("x-ani-parent-request-id", parent)
}

// OperationTimeout is the one configured short phase budget used by current
// authorization, compute resolution and durable acceptance at the BFF.
func (c *ModelDevClient) OperationTimeout() time.Duration {
	if c == nil || c.timeout <= 0 {
		return 3 * time.Second
	}
	return c.timeout
}
func (c *ModelDevClient) admissionFailure(ctx context.Context, callErr, err error, requestID string) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if callErr != nil {
		if c.connection.GetState() != connectivity.Ready {
			return modelDevAdmissionUnavailable()
		}
		return status.FromContextError(callErr).Err()
	}
	return modelDevResolutionError(err, requestID)
}

func validModelDevClientActor(actor string) bool {
	for _, prefix := range []string{"governance:user:", "governance:access-key:"} {
		if strings.HasPrefix(actor, prefix) {
			id, err := strconv.ParseUint(strings.TrimPrefix(actor, prefix), 10, 32)
			return err == nil && id != 0 && actor == prefix+strconv.FormatUint(id, 10)
		}
	}
	return false
}

func modelDevAdmissionUnavailable() error {
	return status.Error(codes.Unavailable, "modeldev admission unavailable")
}

func modelDevResolutionError(err error, requestID string) error {
	failure, ok := status.FromError(err)
	if !ok || len(failure.Proto().ProtoReflect().GetUnknown()) != 0 {
		return modelDevAdmissionUnavailable()
	}
	if len(failure.Proto().Details) == 0 {
		switch failure.Code() {
		case codes.Canceled:
			return status.FromContextError(context.Canceled).Err()
		case codes.DeadlineExceeded:
			return status.FromContextError(context.DeadlineExceeded).Err()
		default:
			return modelDevAdmissionUnavailable()
		}
	}
	wire := failure.Proto()
	if len(wire.Details) != 1 || wire.Details[0] == nil || len(wire.Details[0].ProtoReflect().GetUnknown()) != 0 {
		return modelDevAdmissionUnavailable()
	}
	details := failure.Details()
	if len(details) != 1 {
		return modelDevAdmissionUnavailable()
	}
	detail, ok := details[0].(*modeldevv1.ErrorDetail)
	if !ok || detail == nil || len(detail.ProtoReflect().GetUnknown()) != 0 || len(detail.Violations) != 0 || detail.CorrelationId != requestID {
		return modelDevAdmissionUnavailable()
	}
	var message string
	switch {
	case failure.Code() == codes.InvalidArgument && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT:
		message = "invalid modeldev resolution request"
	case failure.Code() == codes.NotFound && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND:
		message = "input version unavailable"
	case failure.Code() == codes.FailedPrecondition && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_INPUT_NOT_READY:
		message = "input version is not ready"
	case failure.Code() == codes.FailedPrecondition && modelDevFinetuningBusinessReason(detail.Reason):
		message = strings.TrimPrefix(detail.Reason.String(), "ERROR_REASON_")
	case failure.Code() == codes.ResourceExhausted && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_INPUT_VALIDATION_BUSY:
		message = "input validation busy"
	case failure.Code() == codes.PermissionDenied && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_FORBIDDEN:
		message = "source access denied"
	case failure.Code() == codes.AlreadyExists && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT:
		message = "immutable operation conflict"
	case failure.Code() == codes.Unavailable && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_RUNTIME_UNAVAILABLE:
		message = "finetuning runtime unavailable"
	case failure.Code() == codes.Unavailable && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY:
		message = "managed admission environment is not ready"
	case failure.Code() == codes.Unavailable && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE:
		message = "admission resolution unavailable"
	default:
		return modelDevAdmissionUnavailable()
	}
	clean := &modeldevv1.ErrorDetail{Reason: detail.Reason, SafeMessage: message, CorrelationId: requestID}
	if !modelDevCopyLimitDetail(clean, detail) {
		return modelDevAdmissionUnavailable()
	}
	// Remote text is discarded. Only finite, typed limit facts survive.
	rebuilt, detailErr := status.New(failure.Code(), message).WithDetails(clean)
	if detailErr != nil {
		return modelDevAdmissionUnavailable()
	}
	return rebuilt.Err()
}

func modelDevCopyLimitDetail(out, in *modeldevv1.ErrorDetail) bool {
	limitReason := in.Reason == modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED || in.Reason == modeldevv1.ErrorReason_ERROR_REASON_STRUCTURE_LIMIT_EXCEEDED || in.Reason == modeldevv1.ErrorReason_ERROR_REASON_DATASET_RECORD_TOO_LARGE
	if !limitReason {
		return in.Kind == "" && in.LimitName == "" && in.Actual == nil && in.Limit == nil
	}
	if in.Actual == nil || in.Limit == nil || *in.Limit <= 0 || *in.Actual <= *in.Limit {
		return false
	}
	switch in.Kind {
	case "dataset", "base_model", "output", "storage_key":
	default:
		return false
	}
	switch in.LimitName {
	case "max_content_bytes", "tar_read_bound", "max_archive_overhead_bytes", "max_model_files", "max_output_files", "max_archive_entries", "max_relative_path_bytes", "max_storage_key_bytes", "max_manifest_bytes", "max_material_bytes", "max_model_config_bytes", "max_archive_metadata_bytes", "max_record_bytes", "max_json_depth", "max_control_rpc_bytes", "max_publication_candidate_bytes", "max_publication_ref_bytes",
		"MaxModelFiles", "MaxOutputFiles", "MaxArchiveEntries", "MaxRelativePathBytes", "MaxStorageKeyBytes", "MaxManifestBytes", "MaxMaterialBytes", "MaxModelConfigBytes", "MaxArchiveMetadataBytes", "MaxRecordBytes", "MaxJSONDepth", "MaxControlRPCBytes", "MaxPublicationCandidateBytes", "MaxPublicationRefBytes":
	default:
		return false
	}
	if in.Reason == modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED && (in.LimitName != "max_content_bytes" || in.Kind == "storage_key") || in.Reason == modeldevv1.ErrorReason_ERROR_REASON_DATASET_RECORD_TOO_LARGE && (in.Kind != "dataset" || in.LimitName != "MaxRecordBytes") {
		return false
	}
	out.Kind, out.LimitName = in.Kind, in.LimitName
	actual, limit := *in.Actual, *in.Limit
	out.Actual, out.Limit = &actual, &limit
	return true
}
