//go:build s2s_identity_interop

package data

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	integrationv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/integration/v1"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// This opt-in contract test calls the production receivers started by Model's
// TestModelIdentityInteropReceiver and Inference's TestGovernanceIdentityInteropProcess.
// Invalid business input must reach real service validation without persistence.
// The explicit build tag requires both endpoint files; missing receivers fail.
func TestModelInferenceProductionReceiverIdentityInterop(t *testing.T) {
	readConfig := func(variable string) map[string]string {
		t.Helper()
		path := os.Getenv(variable)
		if path == "" {
			t.Fatalf("%s must point to a running production receiver's endpoint file", variable)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]string
		if err := json.Unmarshal(contents, &config); err != nil {
			t.Fatal(err)
		}
		return config
	}
	modelConfig := readConfig("ANI_MODEL_IDENTITY_INTEROP_FILE")
	inferenceConfig := readConfig("ANI_INFERENCE_IDENTITY_INTEROP_FILE")
	const tenant = "10000000-0000-4000-8000-000000000001"
	const actor = "governance:user:7"
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"x-ani-tenant-id", "forged", "x-ani-actor", "forged", "authorization", "not-forwarded",
	))
	assertValidation := func(t *testing.T, err error, message string) {
		t.Helper()
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), message) {
			t.Fatalf("request did not reach production business validation: %v", err)
		}
	}
	t.Run("Model", func(t *testing.T) {
		client, closeClient, err := NewModelClient(ModelClientConfig{
			Address: modelConfig["endpoint"], ServerName: modelConfig["server_name"],
			CAFile: modelConfig["ca_file"], CertFile: modelConfig["cert_file"], KeyFile: modelConfig["key_file"], Timeout: 3 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeClient)
		_, err = client.CreateModel(ctx, tenant, actor, &modelv1.CreateModelRequest{TenantId: "forged-body-tenant"})
		assertValidation(t, err, "invalid model name")
	})
	for _, receiver := range []string{"ordinary", "managed"} {
		t.Run("Inference/"+receiver, func(t *testing.T) {
			client, closeClient, err := NewInferenceClient(InferenceClientConfig{
				Address: inferenceConfig[receiver+"_endpoint"], ServerName: inferenceConfig["server_name"],
				CAFile: inferenceConfig["ca_file"], CertFile: inferenceConfig["client_cert_file"], KeyFile: inferenceConfig["client_key_file"], Timeout: 3 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(closeClient)
			request := &inferencev1.CreateInferenceServiceRequest{}
			if receiver == "managed" {
				request.GpuOwnerAttachment = &integrationv1.GpuOwnerCreateAttachment{Ref: &acceleratorv1.GpuUsageRef{TenantId: tenant}}
			}
			_, err = client.CreateTrusted(ctx, tenant, actor, request)
			assertValidation(t, err, "request_id, name and model_version_id are required")
			_, err = client.GetTrusted(ctx, tenant, actor, &inferencev1.GetInferenceServiceRequest{})
			assertValidation(t, err, "resource_id is required")
			if receiver == "managed" {
				_, err = client.CreateTrusted(ctx, "10000000-0000-4000-8000-000000000099", actor, request)
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("attachment and trusted tenant conflict was accepted: %v", err)
				}
			}
		})
	}
}
