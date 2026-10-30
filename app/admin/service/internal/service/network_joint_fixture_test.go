//go:build networkintegration

package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data"
)

type networkResourcePeer struct {
	ProviderURL, CertificateName, Namespace                                               string
	Address, CAFile, CertFile, KeyFile, RuntimeDSN                                        string
	TenantID, ParentVPC, EntrySubnet, BackendSubnet, BackendAddress, SecondBackendAddress string
	LiveClusterUID, VPCCIDR, SubnetCIDR                                                   string
	ClusterID, ClusterUID, DefaultVPCNamespace, DefaultVPCName, DefaultVPCUID             string
	GatewayID, QualifiedPublicPoolID                                                      string
	Client                                                                                *data.NetworkClient
	DB                                                                                    *sql.DB
}

func newNetworkResourcePeer(t *testing.T) *networkResourcePeer {
	t.Helper()
	if info := os.Getenv("NETWORK_JOINT_EXISTING_RESOURCE"); info != "" {
		// The live runner owns this actual Resource process and its isolated DB.
		// The VPC scenario still uses the same HTTP/auth/mTLS and product cleanup.
		selector := flag.Lookup("test.run").Value.String()
		require.True(t, selector == "^TestNetworkJointHTTP/vpc_presets$" || selector == "^TestNetworkJointHTTP/vpc_presets/rejections$", "live Resource is limited to the VPC product scenarios")
		require.True(t, filepath.IsAbs(info))
		raw, err := os.ReadFile(info)
		require.NoError(t, err)
		peer := &networkResourcePeer{}
		require.NoError(t, json.Unmarshal(raw, peer))
		require.NotEmpty(t, peer.LiveClusterUID)
		require.NotEmpty(t, peer.VPCCIDR)
		require.NotEmpty(t, peer.SubnetCIDR)
		host, _, err := net.SplitHostPort(peer.Address)
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1", host)
		dsn, err := url.Parse(peer.RuntimeDSN)
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1", dsn.Hostname())
		require.True(t, strings.HasPrefix(dsn.Path, "/network_presets_live_"), "live runner must use its exclusive database")
		var closeClient func()
		peer.Client, closeClient, err = data.NewNetworkClient(data.NetworkClientConfig{Address: peer.Address, CAFile: peer.CAFile, CertFile: peer.CertFile, KeyFile: peer.KeyFile, Timeout: 5 * time.Second})
		require.NoError(t, err)
		t.Cleanup(closeClient)
		peer.DB, err = sql.Open("pgx", peer.RuntimeDSN)
		require.NoError(t, err)
		t.Cleanup(func() { _ = peer.DB.Close() })
		return peer
	}
	binary := os.Getenv("RESOURCE_NETWORK_TEST_BINARY")
	require.NotEmpty(t, binary, "exact Resource test binary required")
	require.True(t, filepath.IsAbs(binary))
	dir := t.TempDir()
	cmd := exec.Command(binary, "-test.run=^TestNetworkGovernanceFixture$", "-test.timeout=8m", "-test.v")
	cmd.Env = append(os.Environ(), "NETWORK_JOINT_FIXTURE_DIR="+dir)
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_, _ = fmt.Fprintln(input, "stop")
		_ = input.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Log(output.String())
			}
			require.NoError(t, err, "Resource fixture product cleanup/isolated DB teardown must pass")
			t.Logf("Resource fixture pid=%d exit=0; product and infrastructure cleanup pass", cmd.Process.Pid)
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("Resource fixture exceeded cleanup deadline")
		}
	})
	var raw []byte
	require.Eventually(t, func() bool { raw, err = os.ReadFile(filepath.Join(dir, "fixture.json")); return err == nil }, 30*time.Second, 20*time.Millisecond, "Resource production fixture startup required")
	peer := &networkResourcePeer{}
	require.NoError(t, json.Unmarshal(raw, peer))
	var closeClient func()
	peer.Client, closeClient, err = data.NewNetworkClient(data.NetworkClientConfig{Address: peer.Address, CAFile: peer.CAFile, CertFile: peer.CertFile, KeyFile: peer.KeyFile, Timeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(closeClient)
	peer.DB, err = sql.Open("pgx", peer.RuntimeDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.DB.Close() })
	return peer
}

// Capture only the timed-out LB and its controlled owner chain before cleanup.
// No request credentials, Secret objects, workload env or connection strings
// belong in this diagnostic, and a failed read must not fail the test itself.
func snapshotNetworkLBFailure(ctx context.Context, t *testing.T, peer *networkResourcePeer, lbID string) {
	t.Helper()
	snapshot := map[string]any{"lb_id": lbID, "tenant_id": peer.TenantID, "captured_at": time.Now().UTC()}
	defer func() {
		snapshot["context_error"] = fmt.Sprint(ctx.Err())
		raw, err := json.Marshal(snapshot)
		if err == nil {
			t.Logf("LB timeout snapshot: %s", raw)
		} else {
			t.Logf("LB timeout snapshot encoding unavailable: %T", err)
		}
	}()
	base, err := url.Parse(peer.ProviderURL)
	if err != nil || base.Scheme != "http" || base.Hostname() != "127.0.0.1" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") || peer.LiveClusterUID != "" {
		snapshot["refused"] = "only the owned 127.0.0.1 controlled provider is permitted"
		return
	}
	if peer.DB == nil {
		snapshot["refused"] = "Resource fixture database is unavailable"
		return
	}
	database := map[string]any{}
	rowsByTable := map[string][]map[string]any{}
	snapshot["database"] = database
	for _, table := range []string{"network_provider_bindings", "network_lb_components", "network_reconciliations", "network_lb_generated_resources", "network_lb_members"} {
		var raw []byte
		err := peer.DB.QueryRowContext(ctx, "SELECT coalesce(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (SELECT * FROM "+table+" WHERE tenant_id=$1 AND lb_id=$2) x", peer.TenantID, lbID).Scan(&raw)
		if err != nil {
			database[table] = map[string]any{"read_error_type": fmt.Sprintf("%T", err)}
			continue
		}
		var rows []map[string]any
		if err := json.Unmarshal(raw, &rows); err != nil {
			database[table] = map[string]any{"decode_error_type": fmt.Sprintf("%T", err)}
			continue
		}
		database[table], rowsByTable[table] = rows, rows
	}
	var ingressRaw []byte
	var ingressBindings []map[string]any
	err = peer.DB.QueryRowContext(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(b)),'[]'::jsonb) FROM network_provider_bindings b JOIN network_load_balancers l ON l.tenant_id=b.tenant_id AND (l.public_eip_id=b.eip_id OR l.subnet_id=b.subnet_id) WHERE l.tenant_id=$1 AND l.lb_id=$2`, peer.TenantID, lbID).Scan(&ingressRaw)
	if err == nil {
		err = json.Unmarshal(ingressRaw, &ingressBindings)
	}
	if err != nil {
		database["ingress_bindings"] = map[string]any{"read_error_type": fmt.Sprintf("%T", err)}
	} else {
		database["ingress_bindings"] = ingressBindings
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	requests, native := map[string]any{}, map[string]any{}
	snapshot["controlled_reads"], snapshot["native"] = requests, native
	read := func(path string) map[string]any {
		req, err := http.NewRequestWithContext(ctx, "GET", base.Scheme+"://"+base.Host+path, nil)
		if err != nil {
			requests[path] = map[string]any{"request_error_type": fmt.Sprintf("%T", err)}
			return nil
		}
		response, err := client.Do(req)
		if err != nil {
			requests[path] = map[string]any{"read_error_type": fmt.Sprintf("%T", err)}
			return nil
		}
		defer response.Body.Close()
		requests[path] = map[string]any{"status": response.StatusCode}
		if response.StatusCode != http.StatusOK {
			return nil
		}
		var object map[string]any
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&object); err != nil {
			requests[path] = map[string]any{"status": response.StatusCode, "decode_error_type": fmt.Sprintf("%T", err)}
			return nil
		}
		return object
	}
	paths := map[string]string{
		"Gateway": "/apis/gateway.networking.k8s.io/v1/namespaces/", "HTTPRoute": "/apis/gateway.networking.k8s.io/v1/namespaces/",
		"Backend": "/apis/gateway.envoyproxy.io/v1alpha1/namespaces/", "BackendTrafficPolicy": "/apis/gateway.envoyproxy.io/v1alpha1/namespaces/",
		"EIP": "/apis/networking.kubercloud.com/v1/namespaces/", "Subnet": "/apis/networking.kubercloud.com/v1/namespaces/",
		"Service": "/api/v1/namespaces/", "Pod": "/api/v1/namespaces/", "Deployment": "/apis/apps/v1/namespaces/", "ReplicaSet": "/apis/apps/v1/namespaces/", "EndpointSlice": "/apis/discovery.k8s.io/v1/namespaces/",
	}
	resources := map[string]string{"Gateway": "gateways", "HTTPRoute": "httproutes", "Backend": "backends", "BackendTrafficPolicy": "backendtrafficpolicies", "EIP": "eips", "Subnet": "subnets", "Service": "services", "Deployment": "deployments", "ReplicaSet": "replicasets", "Pod": "pods", "EndpointSlice": "endpointslices"}
	knownUIDs := map[string]bool{}
	collect := func(kind string, row map[string]any) {
		namespace, _ := row["namespace"].(string)
		name, _ := row["provider_name"].(string)
		if namespace == "" || (namespace != peer.Namespace && kind != "EIP" && kind != "Subnet") || name == "" || resources[kind] == "" {
			return
		}
		path := paths[kind] + url.PathEscape(namespace) + "/" + resources[kind] + "/" + url.PathEscape(name)
		if object := read(path); object != nil {
			evidence := networkLBObjectEvidence(object)
			evidence["expected_provider_uid"] = row["provider_uid"]
			native[path] = evidence
			metadata, _ := object["metadata"].(map[string]any)
			if uid, _ := metadata["uid"].(string); uid != "" && kind != "EIP" && kind != "Subnet" {
				knownUIDs[uid] = true
			}
		}
	}
	for _, row := range rowsByTable["network_provider_bindings"] {
		collect("Gateway", row)
	}
	for _, row := range ingressBindings {
		collect(map[string]string{"eip": "EIP", "subnet": "Subnet"}[fmt.Sprint(row["resource_kind"])], row)
	}
	for _, row := range rowsByTable["network_lb_components"] {
		if row["deleted_at"] == nil {
			collect(map[string]string{"route": "HTTPRoute", "policy": "BackendTrafficPolicy", "backend": "Backend"}[fmt.Sprint(row["kind"])], row)
		}
	}
	for _, row := range rowsByTable["network_lb_generated_resources"] {
		if row["namespace"] == peer.Namespace {
			if uid, _ := row["provider_uid"].(string); uid != "" {
				knownUIDs[uid] = true
			}
		}
	}
	// These fixture objects have no labels. Read only this LB's namespace and
	// retain DB identities or the Gateway -> Deployment -> ReplicaSet -> Pod /
	// Gateway -> Service -> EndpointSlice owner chain, never unrelated objects.
	for _, kind := range []string{"Service", "Deployment", "ReplicaSet", "Pod", "EndpointSlice"} {
		if len(knownUIDs) == 0 || ctx.Err() != nil {
			break
		}
		path := paths[kind] + url.PathEscape(peer.Namespace) + "/" + resources[kind]
		list := read(path)
		items, _ := list["items"].([]any)
		for _, item := range items {
			object, _ := item.(map[string]any)
			metadata, _ := object["metadata"].(map[string]any)
			if metadata["namespace"] != peer.Namespace {
				continue
			}
			uid, _ := metadata["uid"].(string)
			matched := uid != "" && knownUIDs[uid]
			owners, _ := metadata["ownerReferences"].([]any)
			for _, owner := range owners {
				reference, _ := owner.(map[string]any)
				ownerUID, _ := reference["uid"].(string)
				matched = matched || knownUIDs[ownerUID]
			}
			if matched {
				native[path+"/"+fmt.Sprint(metadata["name"])] = networkLBObjectEvidence(object)
				knownUIDs[uid] = true
			}
		}
	}
}

func networkLBObjectEvidence(object map[string]any) map[string]any {
	appendValue := func(value any, item any) []any {
		items, _ := value.([]any)
		return append(items, item)
	}
	selectFields := func(source map[string]any, keys ...string) map[string]any {
		out := map[string]any{}
		for _, key := range keys {
			if value, ok := source[key]; ok {
				out[key] = value
			}
		}
		return out
	}
	metadata, _ := object["metadata"].(map[string]any)
	status, _ := object["status"].(map[string]any)
	spec, _ := object["spec"].(map[string]any)
	evidence := selectFields(object, "apiVersion", "kind", "addressType", "ports", "endpoints")
	evidence["metadata"] = selectFields(metadata, "name", "namespace", "uid", "resourceVersion", "generation", "deletionTimestamp", "ownerReferences")
	labels, _ := metadata["labels"].(map[string]any)
	evidence["labels"] = selectFields(labels, "network.ani.io/tenant-id", "network.ani.io/resource-id", "network.ani.io/binding-id", "network.ani.io/managed-by", "kubernetes.io/service-name")
	evidence["status"] = selectFields(status, "conditions", "listeners", "parents", "ancestors", "observedGeneration", "availableReplicas", "updatedReplicas", "readyReplicas", "unavailableReplicas", "phase", "podIP", "podIPs")
	evidence["spec"] = selectFields(spec, "gatewayClassName", "parentRefs", "targetRefs", "type", "endpoints", "ports", "replicas", "selector", "loadBalancer")
	selectedSpec := evidence["spec"].(map[string]any)
	switch object["kind"] {
	case "EIP":
		evidence["spec"] = selectFields(spec, "ipAddress", "ipVersion", "subnet")
		bound, _ := status["boundResource"].(map[string]any)
		evidence["status"].(map[string]any)["boundResource"] = selectFields(bound, "resourceType", "resource", "vpc", "observedGeneration", "nodeName", "disabled")
	case "Subnet":
		evidence["spec"] = selectFields(spec, "type", "cidrBlock", "gateway", "gatewayIP", "ipVersion", "underlayConfig")
		underlay, _ := status["underlayState"].(map[string]any)
		evidence["status"] = selectFields(status, "conditions", "observedGeneration", "boundResources", "v4usingIPrange", "v4availableIPrange")
		evidence["status"].(map[string]any)["underlayState"] = selectFields(underlay, "vlanNetwork", "ready", "chassisNode", "notReadyNodes")
	case "Service":
		annotations, _ := metadata["annotations"].(map[string]any)
		evidence["metadata"].(map[string]any)["annotations"] = selectFields(annotations, "networking.kubercloud.com/lb_vpc", "networking.kubercloud.com/subnet", "networking.kubercloud.com/lb_vip_address", "networking.kubercloud.com/lb_eips")
		for key, value := range selectFields(spec, "clusterIP", "clusterIPs", "externalIPs", "loadBalancerIP", "loadBalancerClass", "externalTrafficPolicy", "internalTrafficPolicy", "healthCheckNodePort", "allocateLoadBalancerNodePorts", "ipFamilies", "ipFamilyPolicy") {
			selectedSpec[key] = value
		}
		evidence["status"].(map[string]any)["loadBalancer"] = status["loadBalancer"]
	}
	if infrastructure, ok := spec["infrastructure"].(map[string]any); ok {
		annotations, _ := infrastructure["annotations"].(map[string]any)
		selectedSpec["network_annotations"] = selectFields(annotations, "networking.kubercloud.com/lb_vpc", "networking.kubercloud.com/subnet", "networking.kubercloud.com/lb_vip_address", "networking.kubercloud.com/lb_eips")
	}
	if health, ok := spec["healthCheck"].(map[string]any); ok {
		active, _ := health["active"].(map[string]any)
		selectedHealth := selectFields(active, "type", "interval", "timeout", "unhealthyThreshold", "healthyThreshold")
		overrides, _ := active["overrides"].(map[string]any)
		selectedHealth["overrides"] = selectFields(overrides, "port")
		selectedSpec["healthCheck"] = selectedHealth
	}
	if listeners, ok := spec["listeners"].([]any); ok {
		for _, raw := range listeners {
			listener, _ := raw.(map[string]any)
			selectedSpec["listeners"] = appendValue(selectedSpec["listeners"], selectFields(listener, "name", "protocol", "port"))
		}
	}
	if rules, ok := spec["rules"].([]any); ok {
		for _, raw := range rules {
			rule, _ := raw.(map[string]any)
			refs, _ := rule["backendRefs"].([]any)
			for _, rawRef := range refs {
				ref, _ := rawRef.(map[string]any)
				selectedSpec["backendRefs"] = appendValue(selectedSpec["backendRefs"], selectFields(ref, "group", "kind", "namespace", "name", "port", "weight"))
			}
		}
	}
	if template, ok := spec["template"].(map[string]any); ok {
		spec, _ = template["spec"].(map[string]any)
	}
	if containers, ok := spec["containers"].([]any); ok {
		for _, raw := range containers {
			container, _ := raw.(map[string]any)
			selectedSpec["containers"] = appendValue(selectedSpec["containers"], selectFields(container, "name", "ports"))
		}
	}
	if containers, ok := status["containerStatuses"].([]any); ok {
		for _, raw := range containers {
			container, _ := raw.(map[string]any)
			evidence["containerStatuses"] = appendValue(evidence["containerStatuses"], selectFields(container, "name", "ready", "restartCount"))
		}
	}
	return evidence
}
