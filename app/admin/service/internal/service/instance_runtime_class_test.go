package service

import (
	"bytes"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"google.golang.org/protobuf/proto"
	"testing"
)

// RuntimeClass comes only from ACC's accepted execution snapshot.
func TestInstanceGpuRuntimeClassFrozenCanonical(t *testing.T) {
	p, _, _ := gpuVector(t)
	b := instanceSettingsBusiness(t)
	b.ComputeType = "gpu"
	b.GpuExecution = p.Resolved.Execution
	b.GpuDisplay = p.Resolved.Selection
	raw, e := res.CanonicalBusinessPayload(b)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(raw, []byte(`"runtime_class_name":"nvidia"`)) {
		t.Fatal("managed runtime class absent")
	}
	f, e := res.DecodeBusinessPayload(raw)
	if e != nil || !proto.Equal(f, b) {
		t.Fatal(e)
	}
	before, _ := res.BusinessPayloadDigest(f)
	b.GpuExecution.RuntimeClassName = "nvidia-next"
	after, _ := res.BusinessPayloadDigest(b)
	if before == after || f.GpuExecution.RuntimeClassName != "nvidia" {
		t.Fatal("snapshot mutated")
	}
}
