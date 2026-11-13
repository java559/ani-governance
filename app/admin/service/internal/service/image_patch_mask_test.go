package service

import (
	"testing"

	admin "go-wind-admin/api/gen/go/admin/service/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func pathsMask(paths ...string) *fieldmaskpb.FieldMask {
	if paths == nil {
		return nil
	}
	return &fieldmaskpb.FieldMask{Paths: paths}
}

// update_mask 的 wire 格式必须是 camelCase：protojson 的 FieldMask 解码器
// 会把 JSON 名归一化为 snake_case，而纯 snake_case 会被它直接判为非法路径。
// 因此 Gov 服务层拿到的 Paths 永远是规范蛇形，映射表必须按蛇形建键。
func TestUpdatePlatformImageMaskUsesCanonicalSnakeCase(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"data":{"displayName":"x","expectedVersion":"3","idempotencyKey":"k"},"update_mask":"displayName"}`, "display_name"},
		{`{"data":{"description":"x","expectedVersion":"3","idempotencyKey":"k"},"update_mask":"description"}`, "description"},
		{`{"data":{"purposes":["training"],"expectedVersion":"3","idempotencyKey":"k"},"update_mask":"purposes"}`, "purposes"},
		{`{"data":{"accelerator":"nvidia","expectedVersion":"3","idempotencyKey":"k"},"update_mask":"accelerator"}`, "accelerator"},
	}
	for _, c := range cases {
		var req admin.UpdatePlatformImageRequest
		if err := protojson.Unmarshal([]byte(c.raw), &req); err != nil {
			t.Fatalf("decode %s: %v", c.raw, err)
		}
		if got := req.GetUpdateMask().GetPaths(); len(got) != 1 || got[0] != c.want {
			t.Fatalf("mask paths = %v, want [%s]", got, c.want)
		}
	}
}

// TestImagePatchMaskAcceptsEveryMutablePath 覆盖全部四个可改字段。
func TestImagePatchMaskAcceptsEveryMutablePath(t *testing.T) {
	for _, path := range []string{"display_name", "description", "purposes", "accelerator"} {
		if _, err := imagePatchMask(pathsMask(path)); err != nil {
			t.Fatalf("path %s rejected: %v", path, err)
		}
	}
}

// TestImagePatchMaskRejectsUnknownAndDuplicate 覆盖拒绝分支。
func TestImagePatchMaskRejectsUnknownAndDuplicate(t *testing.T) {
	for _, mask := range [][]string{
		nil,
		{},
		{"*"},
		{"displayName"},
		{"data.display_name"},
		{"display_name", "display_name"},
		{"display_name", "description", "purposes", "accelerator", "extra"},
	} {
		if _, err := imagePatchMask(pathsMask(mask...)); err == nil {
			t.Fatalf("mask %v should be rejected", mask)
		}
	}
}
