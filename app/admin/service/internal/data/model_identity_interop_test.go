//go:build s2s_identity_interop

package data

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// This opt-in contract test calls the production receiver started by Model's
// TestModelIdentityInteropReceiver.
// Invalid business input must reach real service validation without persistence.
// The explicit build tag requires the endpoint file; a missing receiver fails.
func TestModelProductionReceiverIdentityInterop(t *testing.T) {
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
}
