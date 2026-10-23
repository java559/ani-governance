package data

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const GovernanceAcceleratorDNS = "ani-governance"

type AcceleratorClientConfig struct {
	Address, ServerName, CAFile, CertFile, KeyFile string
	Timeout                                        time.Duration
}

// AcceleratorClient exposes only the public module's generated contracts.
// The interceptor reconstructs all outgoing identity metadata from typed
// delegated contexts. User credentials and public identity headers are dropped.
type AcceleratorClient struct {
	Admin   acc.AcceleratorAdminServiceClient
	Catalog acc.AcceleratorCatalogServiceClient
	Usage   acc.AcceleratorUsageServiceClient
}

func AcceleratorConfigFromEnv() (AcceleratorClientConfig, error) {
	c := AcceleratorClientConfig{Address: os.Getenv("ANI_ACCELERATOR_ADDR"), ServerName: os.Getenv("ANI_ACCELERATOR_SERVER_NAME"), CAFile: os.Getenv("ANI_ACCELERATOR_CA"), CertFile: os.Getenv("ANI_ACCELERATOR_CERT"), KeyFile: os.Getenv("ANI_ACCELERATOR_KEY"), Timeout: 3 * time.Second}
	if value := os.Getenv("ANI_ACCELERATOR_TIMEOUT"); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil {
			return c, err
		}
		c.Timeout = d
	}
	return c, nil
}

func NewAcceleratorClient(c AcceleratorClientConfig) (*AcceleratorClient, func(), error) {
	if c.Address == "" || strings.TrimSpace(c.Address) != c.Address || c.ServerName == "" || strings.ContainsAny(c.ServerName, "*:/\\ \t\r\n") || c.Timeout <= 0 {
		return nil, nil, fmt.Errorf("accelerator address, exact server DNS name and positive timeout required")
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, nil, fmt.Errorf("accelerator CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, nil, fmt.Errorf("accelerator CA has no certificates")
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("accelerator client identity: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	if !acceleratorGovernanceCertificate(leaf) {
		return nil, nil, fmt.Errorf("accelerator client certificate requires unique exact Governance DNS SAN")
	}
	var conn *grpc.ClientConn
	interceptor := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		md, err := acceleratorMetadata(method, req)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, md), c.Timeout)
		defer cancel()
		return invoke(ctx, method, req, reply, cc, opts...)
	}
	conn, err = grpc.NewClient(c.Address, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithUnaryInterceptor(interceptor), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: c.ServerName, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !hasExactDNSName(state.PeerCertificates[0], c.ServerName) {
			return fmt.Errorf("accelerator server exact DNS SAN mismatch")
		}
		return nil
	}})))
	if err != nil {
		return nil, nil, err
	}
	return &AcceleratorClient{Admin: acc.NewAcceleratorAdminServiceClient(conn), Catalog: acc.NewAcceleratorCatalogServiceClient(conn), Usage: acc.NewAcceleratorUsageServiceClient(conn)}, func() { _ = conn.Close() }, nil
}

// SyncGpuUsage intentionally has no delegated actor/action/tenant metadata.
// Its authority is the exact service certificate plus persisted ledger facts.
func (c *AcceleratorClient) SyncGpuUsage(ctx context.Context, r *acc.SyncGpuUsageRequest) (*acc.SyncGpuUsageResponse, error) {
	return c.Usage.SyncGpuUsage(ctx, r)
}

func acceleratorMetadata(method string, request any) (metadata.MD, error) {
	invalid := func() (metadata.MD, error) {
		return nil, status.Error(codes.PermissionDenied, "invalid accelerator delegation")
	}
	kind := acceleratorMethodKind(method)
	if kind == "" {
		return invalid()
	}
	action := method[strings.LastIndexByte(method, '/')+1:]
	if method == acc.AcceleratorUsageService_SyncGpuUsage_FullMethodName {
		r, ok := request.(*acc.SyncGpuUsageRequest)
		if !ok || r == nil || r.RequestId == "" || r.Projection == nil {
			return invalid()
		}
		return metadata.MD{}, nil
	}
	var actor *acc.Actor
	var tenant, requestID string
	switch r := request.(type) {
	case interface{ GetMutation() *acc.AdminMutation }:
		if kind != "admin" {
			return invalid()
		}
		m := r.GetMutation()
		if m == nil || m.Context == nil {
			return invalid()
		}
		actor = m.Context.Actor
		requestID = m.Context.RequestId
	case interface{ GetContext() *acc.AdminRead }:
		if kind != "admin" {
			return invalid()
		}
		a := r.GetContext()
		if a == nil {
			return invalid()
		}
		actor = a.Actor
		requestID = a.RequestId
	case interface{ GetContext() *acc.TenantContext }:
		if kind != "tenant" {
			return invalid()
		}
		t := r.GetContext()
		if t == nil {
			return invalid()
		}
		actor = t.Actor
		tenant = t.TenantId
		requestID = t.RequestId
		id, err := uuid.Parse(tenant)
		if err != nil || id == uuid.Nil || id.String() != tenant {
			return invalid()
		}
	default:
		return invalid()
	}
	if actor == nil || actor.Type != "user" || actor.Id == "" || requestID == "" {
		return invalid()
	}
	md := metadata.Pairs("x-ani-action", action, "x-ani-actor-type", actor.Type, "x-ani-actor-id", actor.Id)
	if tenant != "" {
		md.Set("x-ani-tenant-id", tenant)
	}
	return md, nil
}

// The trusted client only exposes registered Governance calls. A matching
// service prefix or short method name never grants access to another RPC.
func acceleratorMethodKind(method string) string {
	switch method {
	case acc.AcceleratorAdminService_GetInitialization_FullMethodName,
		acc.AcceleratorAdminService_ListDiscoveredNodes_FullMethodName,
		acc.AcceleratorAdminService_CreateInitialization_FullMethodName,
		acc.AcceleratorAdminService_GetInitializationOperation_FullMethodName,
		acc.AcceleratorAdminService_RetryInitializationOperation_FullMethodName,
		acc.AcceleratorAdminService_RegisterCluster_FullMethodName,
		acc.AcceleratorAdminService_ListClusters_FullMethodName,
		acc.AcceleratorAdminService_CreatePool_FullMethodName,
		acc.AcceleratorAdminService_ListPools_FullMethodName,
		acc.AcceleratorAdminService_AdoptSupplyGroup_FullMethodName,
		acc.AcceleratorAdminService_ListSupplyGroups_FullMethodName,
		acc.AcceleratorAdminService_SetAdmission_FullMethodName,
		acc.AcceleratorAdminService_CreateProfile_FullMethodName,
		acc.AcceleratorAdminService_PreviewProfile_FullMethodName,
		acc.AcceleratorAdminService_SetProfileState_FullMethodName,
		acc.AcceleratorAdminService_DeleteProfile_FullMethodName,
		acc.AcceleratorAdminService_AdminListProfiles_FullMethodName,
		acc.AcceleratorAdminService_AdminListBindings_FullMethodName,
		acc.AcceleratorAdminService_ListDevices_FullMethodName,
		acc.AcceleratorAdminService_AdminGetCapacity_FullMethodName:
		return "admin"
	case acc.AcceleratorCatalogService_ListProfiles_FullMethodName,
		acc.AcceleratorCatalogService_GetProfile_FullMethodName,
		acc.AcceleratorCatalogService_ResolveGpuRequest_FullMethodName,
		acc.AcceleratorCatalogService_GetCapacity_FullMethodName,
		acc.AcceleratorCatalogService_CheckGpuFit_FullMethodName,
		acc.AcceleratorUsageService_GetGpuUsage_FullMethodName,
		acc.AcceleratorUsageService_ListGpuUsages_FullMethodName,
		acc.AcceleratorUsageService_ListBindings_FullMethodName:
		return "tenant"
	case acc.AcceleratorUsageService_SyncGpuUsage_FullMethodName:
		return "fact"
	default:
		return ""
	}
}

// Inspect every GeneralName, including SAN types crypto/x509 does not expose.
// A valid Governance identity contains exactly one DNS name and no other SAN.
func acceleratorGovernanceCertificate(cert *x509.Certificate) bool {
	if !governanceClientCertificate(cert) {
		return false
	}
	for _, extension := range cert.Extensions {
		if !extension.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) {
			continue
		}
		var names []asn1.RawValue
		rest, err := asn1.Unmarshal(extension.Value, &names)
		return err == nil && len(rest) == 0 && len(names) == 1 && names[0].Class == asn1.ClassContextSpecific && names[0].Tag == 2 && !names[0].IsCompound && string(names[0].Bytes) == GovernanceAcceleratorDNS
	}
	return false
}
