package service

import (
	"strings"
	"testing"

	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// validateImageCredential 允许「恢复后冻结」的首次签发凭据：Res 的
// BlockRecoveredCredential 只把 state 改成 blocked 并让 version 自增，
// generation / username / expiresAt 仍是签发前的空值。若 Gov 只放行 issuing，
// 管理员拿不到重新签发所需的 expectedVersion，接口会一直 503。
func TestValidateImageCredentialAcceptsRecoveredBlockedCandidate(t *testing.T) {
	cases := []struct {
		name string
		v    *imagev1.PublisherCredential
		ok   bool
	}{
		{
			name: "恢复后 blocked 且 generation=0 可读",
			v:    &imagev1.PublisherCredential{SpaceId: "6f91947d-28d0-4b59-9d29-e9df29b2e989", State: "blocked", Generation: 0, Version: 3, UpdatedAt: timestamppb.Now()},
			ok:   true,
		},
		{
			name: "中断中 issuing 且 generation=0 仍可读",
			v:    &imagev1.PublisherCredential{SpaceId: "6f91947d-28d0-4b59-9d29-e9df29b2e989", State: "issuing", Generation: 0, Version: 2, UpdatedAt: timestamppb.Now()},
			ok:   true,
		},
		{
			name: "blocked 带已有的 generation 走常规校验",
			v:    &imagev1.PublisherCredential{SpaceId: "6f91947d-28d0-4b59-9d29-e9df29b2e989", State: "blocked", Generation: 2, Version: 5, Username: "robot$i-x", UpdatedAt: timestamppb.Now()},
			ok:   true,
		},
		{
			name: "blocked 且 generation=0 但有用户名仍拒绝",
			v:    &imagev1.PublisherCredential{SpaceId: "6f91947d-28d0-4b59-9d29-e9df29b2e989", State: "blocked", Generation: 0, Version: 3, Username: "robot$i-x", UpdatedAt: timestamppb.Now()},
			ok:   false,
		},
		{
			name: "active 且 generation=0 仍拒绝",
			v:    &imagev1.PublisherCredential{SpaceId: "6f91947d-28d0-4b59-9d29-e9df29b2e989", State: "active", Generation: 0, Version: 3, UpdatedAt: timestamppb.Now()},
			ok:   false,
		},
		{
			name: "未知 state 仍拒绝",
			v:    &imagev1.PublisherCredential{SpaceId: "6f91947d-28d0-4b59-9d29-e9df29b2e989", State: "weird", Generation: 0, Version: 3, UpdatedAt: timestamppb.Now()},
			ok:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateImageCredential(c.v, "")
			if c.ok && err != nil {
				t.Fatalf("unexpected reject: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("expected reject, got nil")
			}
		})
	}
}

// 平台项目名上限必须与 Res 的 PlatformProject 校验（3–48）一致，否则 43–48
// 字符的项目名能在 Res 开通，Gov 读回包时却 503。
func TestValidatePlatformSpaceProjectNameLength(t *testing.T) {
	base := func(name string) *imagev1.ImageSpace {
		return &imagev1.ImageSpace{SpaceId: "6f91947d-28d0-4b59-9d29-e9df29b2e989", ProjectName: name, RegistryAuthority: "10.10.1.66:30003", State: "available", Version: 1, PullCredentialGeneration: 0, CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}
	}
	cases := []struct {
		name string
		ok   bool
	}{
		{"ani-" + strings.Repeat("a", 44), true}, // 48
		{"ani-" + strings.Repeat("a", 45), false},
		{"ani-" + strings.Repeat("a", 41), true}, // 45，旧上限 42 会拒
	}
	for _, c := range cases {
		err := validatePlatformSpace(base(c.name))
		if c.ok && err != nil {
			t.Fatalf("%d 字符应通过: %v", len(c.name), err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%d 字符应拒绝", len(c.name))
		}
	}
}
