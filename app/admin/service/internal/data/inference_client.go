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
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// InferenceClientConfig is controlled deployment configuration, never user
// input. Both the endpoint and exact server certificate DNS name are required.
type InferenceClientConfig struct {
	Address, CAFile, CertFile, KeyFile, ServerName string
	Timeout                                        time.Duration
}

func InferenceConfigFromEnv() (InferenceClientConfig, error) {
	c := InferenceClientConfig{Address: os.Getenv("ANI_INFERENCE_ADDR"), CAFile: os.Getenv("ANI_INFERENCE_CA_FILE"), CertFile: os.Getenv("ANI_INFERENCE_CERT_FILE"), KeyFile: os.Getenv("ANI_INFERENCE_KEY_FILE"), ServerName: os.Getenv("ANI_INFERENCE_SERVER_NAME"), Timeout: 3 * time.Second}
	if c.Address == "" {
		return c, nil
	}
	if strings.TrimSpace(c.Address) != c.Address || c.CAFile == "" || c.CertFile == "" || c.KeyFile == "" || c.ServerName == "" || strings.ContainsAny(c.ServerName, "*:/\\ \t\n") {
		return c, errors.New("inference client requires managed address, CA, certificate, key and exact server DNS name")
	}
	return c, nil
}

type InferenceClient struct {
	client      runtimev1.InferenceServiceManagerClient
	diagnostics runtimev1.InferenceDiagnosticsClient
	timeout     time.Duration
}

func NewInferenceClient(c InferenceClientConfig) (*InferenceClient, func(), error) {
	if c.Address == "" || c.ServerName == "" || strings.TrimSpace(c.Address) != c.Address || strings.ContainsAny(c.ServerName, "*:/\\ \t\n") || c.Timeout <= 0 || c.Timeout > 3*time.Second {
		return nil, nil, errors.New("invalid inference client configuration")
	}
	certificate, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, nil, errors.New("inference client certificate unavailable")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || !governanceClientCertificate(leaf) {
		return nil, nil, errors.New("inference client requires the registered Governance DNS identity")
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, nil, errors.New("inference client CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, nil, errors.New("inference client CA contains no certificates")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: c.ServerName,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return errors.New("unverified inference server identity")
			}
			if !hasExactDNSName(state.PeerCertificates[0], c.ServerName) {
				return errors.New("inference server exact DNS SAN mismatch")
			}
			return nil
		},
	}
	connection, err := grpc.NewClient(c.Address, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return nil, nil, errors.New("invalid inference client address")
	}
	return &InferenceClient{client: runtimev1.NewInferenceServiceManagerClient(connection), diagnostics: runtimev1.NewInferenceDiagnosticsClient(connection), timeout: c.Timeout}, func() { _ = connection.Close() }, nil
}

func (c *InferenceClient) Create(ctx context.Context, request *runtimev1.CreateInferenceServiceRequest) (*runtimev1.OperationResponse, error) {
	call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.MD{}), c.timeout)
	defer cancel()
	return c.client.CreateInferenceService(call, request)
}

func (c *InferenceClient) Delete(ctx context.Context, request *runtimev1.DeleteInferenceServiceRequest) (*runtimev1.OperationResponse, error) {
	call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.MD{}), c.timeout)
	defer cancel()
	return c.client.DeleteInferenceService(call, request)
}

// InferenceTrustedOwnerClient is the tenant-bound surface used by Governance
// for normal and quota-dispatched commands. The legacy Create/Delete methods
// remain for the existing GPU contract adapter; new calls must use these
// methods so the server receives verified tenant and actor metadata.
type InferenceTrustedOwnerClient interface {
	CreateTrusted(context.Context, string, string, *runtimev1.CreateInferenceServiceRequest) (*runtimev1.OperationResponse, error)
	DeleteTrusted(context.Context, string, string, *runtimev1.DeleteInferenceServiceRequest) (*runtimev1.OperationResponse, error)
	GetTrusted(context.Context, string, string, *runtimev1.GetInferenceServiceRequest) (*runtimev1.InferenceService, error)
	ListTrusted(context.Context, string, string, *runtimev1.ListInferenceServicesRequest) (*runtimev1.ListInferenceServicesResponse, error)
	GetOperationTrusted(context.Context, string, string, *runtimev1.GetOperationRequest) (*runtimev1.Operation, error)
	UpdateTrusted(context.Context, string, string, *runtimev1.UpdateInferenceServiceRequest) (*runtimev1.OperationResponse, error)
	StartTrusted(context.Context, string, string, *runtimev1.ServiceCommandRequest) (*runtimev1.OperationResponse, error)
	StopTrusted(context.Context, string, string, *runtimev1.ServiceCommandRequest) (*runtimev1.OperationResponse, error)
	RestartTrusted(context.Context, string, string, *runtimev1.ServiceCommandRequest) (*runtimev1.OperationResponse, error)
	ListOperationsTrusted(context.Context, string, string, *runtimev1.ListOperationsRequest) (*runtimev1.ListOperationsResponse, error)
}

func validInferenceIdentity(tenant, actor string) bool {
	id, err := uuid.Parse(tenant)
	if err != nil || id == uuid.Nil || id.String() != tenant {
		return false
	}
	for _, prefix := range []string{"governance:user:", "governance:access-key:"} {
		if strings.HasPrefix(actor, prefix) {
			n, parseErr := strconv.ParseUint(strings.TrimPrefix(actor, prefix), 10, 32)
			return parseErr == nil && n > 0 && actor == prefix+strconv.FormatUint(n, 10)
		}
	}
	return false
}

func (c *InferenceClient) trustedContext(ctx context.Context, tenant, actor string) (context.Context, context.CancelFunc, error) {
	if c == nil || c.client == nil || c.timeout <= 0 {
		return nil, nil, errors.New("inference client unavailable")
	}
	if !validInferenceIdentity(tenant, actor) {
		return nil, nil, errors.New("invalid trusted inference identity")
	}
	call, cancel := context.WithTimeout(ctx, c.timeout)
	call = metadata.NewOutgoingContext(call, metadata.Pairs(
		"x-ani-tenant-id", tenant,
		"x-ani-actor", actor,
		"x-ani-request-id", uuid.NewString(),
	))
	return call, cancel, nil
}

func (c *InferenceClient) CreateTrusted(ctx context.Context, tenant, actor string, request *runtimev1.CreateInferenceServiceRequest) (*runtimev1.OperationResponse, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.CreateInferenceService(call, request, grpc.WaitForReady(true))
}

func (c *InferenceClient) DeleteTrusted(ctx context.Context, tenant, actor string, request *runtimev1.DeleteInferenceServiceRequest) (*runtimev1.OperationResponse, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.DeleteInferenceService(call, request, grpc.WaitForReady(true))
}

func (c *InferenceClient) GetTrusted(ctx context.Context, tenant, actor string, request *runtimev1.GetInferenceServiceRequest) (*runtimev1.InferenceService, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.GetInferenceService(call, request, grpc.WaitForReady(true))
}

func (c *InferenceClient) ListTrusted(ctx context.Context, tenant, actor string, request *runtimev1.ListInferenceServicesRequest) (*runtimev1.ListInferenceServicesResponse, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.ListInferenceServices(call, request, grpc.WaitForReady(true))
}

func (c *InferenceClient) GetOperationTrusted(ctx context.Context, tenant, actor string, request *runtimev1.GetOperationRequest) (*runtimev1.Operation, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.GetOperation(call, request, grpc.WaitForReady(true))
}

func (c *InferenceClient) UpdateTrusted(ctx context.Context, tenant, actor string, request *runtimev1.UpdateInferenceServiceRequest) (*runtimev1.OperationResponse, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.UpdateInferenceService(call, request, grpc.WaitForReady(true))
}

func (c *InferenceClient) StartTrusted(ctx context.Context, tenant, actor string, request *runtimev1.ServiceCommandRequest) (*runtimev1.OperationResponse, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.StartInferenceService(call, request, grpc.WaitForReady(true))
}

func (c *InferenceClient) StopTrusted(ctx context.Context, tenant, actor string, request *runtimev1.ServiceCommandRequest) (*runtimev1.OperationResponse, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.StopInferenceService(call, request, grpc.WaitForReady(true))
}

func (c *InferenceClient) RestartTrusted(ctx context.Context, tenant, actor string, request *runtimev1.ServiceCommandRequest) (*runtimev1.OperationResponse, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.RestartInferenceService(call, request, grpc.WaitForReady(true))
}

func (c *InferenceClient) ListOperationsTrusted(ctx context.Context, tenant, actor string, request *runtimev1.ListOperationsRequest) (*runtimev1.ListOperationsResponse, error) {
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	defer cancel()
	return c.client.ListOperations(call, request, grpc.WaitForReady(true))
}
