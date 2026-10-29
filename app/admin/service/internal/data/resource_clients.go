package data

import (
	"fmt"
	"os"

	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	instance "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	sandbox "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/v1"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// NewResourceClientsFromEnv shares the managed Resource listener and owns its
// one connection. Neither domain client can close a connection used by another.
func NewResourceClientsFromEnv() (*InstanceClient, *ComputeClient, func(), error) {
	address := os.Getenv("ANI_INSTANCE_ADDR")
	if address == "" {
		return nil, nil, func() {}, nil
	}
	tls, err := resourceClientTLS(os.Getenv("ANI_INSTANCE_CA"), os.Getenv("ANI_INSTANCE_CERT"), os.Getenv("ANI_INSTANCE_KEY"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resource mTLS configuration invalid: %w", err)
	}
	connection, err := grpc.NewClient(address, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(tls)))
	if err != nil {
		return nil, nil, nil, err
	}
	return &InstanceClient{client: instance.NewInstanceServiceClient(connection), sandbox: sandbox.NewSandboxServiceClient(connection), vm: &VMClient{client: vm.NewVMServiceClient(connection)}},
		&ComputeClient{client: compute.NewComputePresetServiceClient(connection)},
		func() { _ = connection.Close() }, nil
}
