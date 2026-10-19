package service

import (
	"bytes"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestInstanceSharedCanonicalAndFrozenParameters(t *testing.T) {
	b := instanceSettingsBusiness(t)
	original, e := res.BusinessPayloadDigest(b)
	if e != nil {
		t.Fatal(e)
	}
	for name, mutate := range map[string]func(*res.CreateSpec){"cpu": func(b *res.CreateSpec) { b.Cpu = "2000m" }, "memory": func(b *res.CreateSpec) { b.Memory = "4096Mi" }, "image": func(b *res.CreateSpec) { b.ImageReference += "-changed" }, "cluster": func(b *res.CreateSpec) { b.ClusterId = "other" }, "namespace": func(b *res.CreateSpec) { b.Namespace += "-changed" }, "workspace": func(b *res.CreateSpec) { b.WorkspaceSize = "2Gi" }, "account": func(b *res.CreateSpec) { b.ServiceAccount = "other" }, "storage": func(b *res.CreateSpec) { b.StorageClass = "other" }, "uid": func(b *res.CreateSpec) { b.Uid++ }, "gid": func(b *res.CreateSpec) { b.Gid++ }, "shell": func(b *res.CreateSpec) { b.Shell = "/bin/bash" }, "placement": func(b *res.CreateSpec) { b.Scheduling.PodPlacement = res.SchedulingOptions_SPREAD_PREFERRED.Enum() }, "priority": func(b *res.CreateSpec) { b.Scheduling.PriorityClassName = proto.String("normal") }} {
		t.Run(name, func(t *testing.T) {
			c := proto.Clone(b).(*res.CreateSpec)
			mutate(c)
			d, e := res.BusinessPayloadDigest(c)
			if e != nil || d == original {
				t.Fatal(d, e)
			}
		})
	}
	raw, e := res.CanonicalBusinessPayload(b)
	if e != nil {
		t.Fatal(e)
	}
	f, e := res.DecodeBusinessPayload(raw)
	if e != nil || !proto.Equal(f, b) {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("original_charges")) {
		t.Fatal("ledger leaked")
	}
	data := instanceSettingsData(b)
	before, _ := res.BusinessPayloadDigest(data)
	data.Scheduling = &res.SchedulingOptions{PodPlacement: res.SchedulingOptions_NONE.Enum()}
	after, _ := res.BusinessPayloadDigest(data)
	if before == after {
		t.Fatal("original presence lost")
	}
}
