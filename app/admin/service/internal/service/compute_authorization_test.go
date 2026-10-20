package service

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/proto"
)

func TestComputePlatformPrincipal(t *testing.T) {
	const permission = "compute:preset:admin:update"
	valid := func() *auth.Principal {
		claims := &authv1.UserTokenPayload{UserId: 9, Roles: []string{"platform:catalog"}, Permissions: []string{permission}}
		return &auth.Principal{Type: auth.SubjectUser, ID: 9, Roles: claims.Roles, Claims: claims}
	}
	for _, tc := range []struct {
		name   string
		change func(*auth.Principal)
		code   int
	}{
		{name: "exact platform grant"},
		{name: "API key", change: func(p *auth.Principal) { p.Type = auth.SubjectAPIKey }, code: 403},
		{name: "tenant principal", change: func(p *auth.Principal) { p.TenantID = 7 }, code: 403},
		{name: "tenant claims", change: func(p *auth.Principal) { p.Claims.TenantId = proto.Uint32(7) }, code: 403},
		{name: "different user claims", change: func(p *auth.Principal) { p.Claims.UserId = 10 }, code: 403},
		{name: "missing claims", change: func(p *auth.Principal) { p.Claims = nil }, code: 403},
		{name: "tenant role", change: func(p *auth.Principal) { p.Roles = []string{"tenant:manager"} }, code: 403},
		{name: "platform role alone", change: func(p *auth.Principal) { p.Claims.Permissions = nil }, code: 403},
		{name: "old permission", change: func(p *auth.Principal) { p.Claims.Permissions = []string{"instance:preset:admin:update"} }, code: 403},
		{name: "different action", change: func(p *auth.Principal) { p.Claims.Permissions = []string{"compute:preset:admin:read"} }, code: 403},
		{name: "wildcard", change: func(p *auth.Principal) { p.Claims.Permissions = []string{"compute:preset:admin:*"} }, code: 403},
		{name: "zero subject", change: func(p *auth.Principal) { p.ID = 0 }, code: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid()
			if tc.change != nil {
				tc.change(p)
			}
			got, err := computePlatformPrincipal(auth.NewPrincipalContext(context.Background(), p), permission)
			if tc.code != 0 {
				if errors.Code(err) != tc.code || got != nil {
					t.Fatalf("permission rejection: principal=%v error=%v", got, err)
				}
			} else if err != nil || got != p {
				t.Fatalf("exact permission rejected: %v", err)
			}
		})
	}
	if _, err := computePlatformPrincipal(context.Background(), permission); errors.Code(err) != 401 {
		t.Fatalf("unauthenticated request: %v", err)
	}
}
