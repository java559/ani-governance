package data

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// LG10 requires every one of the 10 native grpc.NewClient construction points to
// use the shared instrumentation. This guards against a new connection being
// added without the trace/completion option, which the runtime test cannot catch
// because it only exercises one dial.
func TestAllOutboundClientsUseSharedInstrumentation(t *testing.T) {
	// file -> expected peer constant, as written at the call site
	want := map[string]string{
		"resource_clients.go":        "peerResourceService",
		"image_client.go":            "peerResourceService",
		"network_client.go":          "peerResourceService",
		"storage_client.go":          "peerResourceService",
		"instance_session_client.go": "peerResourceService",
		"accelerator_client.go":      "peerAcceleratorService",
		"inference_client.go":        "peerInferenceService",
		"model_client.go":            "peerModelService",
		"modeldev_client.go":         "peerModelDevService",
		"observability_client.go":    "peerObservabilityService",
	}
	if len(want) != 10 {
		t.Fatalf("the plan fixes 10 outbound constructors, got %d", len(want))
	}

	newClient := regexp.MustCompile(`grpc\.NewClient\(`)
	for file, peer := range want {
		src, err := os.ReadFile(filepath.Join(".", file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(src)

		calls := newClient.FindAllString(text, -1)
		if len(calls) != 1 {
			t.Errorf("%s: expected exactly 1 grpc.NewClient, found %d", file, len(calls))
			continue
		}
		if !strings.Contains(text, "outboundDial(") {
			t.Errorf("%s: grpc.NewClient does not go through outboundDial", file)
			continue
		}
		if !strings.Contains(text, "outboundDial("+peer) {
			t.Errorf("%s: outboundDial does not use the fixed peer constant %q", file, peer)
		}
	}

	// The single option builder must be the only place a stats handler is added.
	obs, err := os.ReadFile("rpc_observability.go")
	if err != nil {
		t.Fatalf("read rpc_observability.go: %v", err)
	}
	statsHandler := regexp.MustCompile(`grpc\.WithStatsHandler\(`)
	if n := len(statsHandler.FindAllString(string(obs), -1)); n != 1 {
		t.Errorf("expected exactly one grpc.WithStatsHandler in rpc_observability.go, found %d", n)
	}
}

// A new native connection that bypasses the builder would silently lose trace
// propagation and the completion record; this pins the invariant for the whole
// data package, not just the ten known files.
func TestNoRawNewOutsideBuilder(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "grpc.NewClient(") && !strings.Contains(line, "grpc.Dial(") {
				continue
			}
			if strings.Contains(line, "outboundDial(") {
				continue
			}
			t.Errorf("%s: raw grpc client construction without outboundDial: %s", f, strings.TrimSpace(line))
		}
	}
}
