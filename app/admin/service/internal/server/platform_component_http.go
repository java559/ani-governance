package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
)

// platformComponentPayload 用手写 struct 输出：契约要求 replicas 等字段在
// 不可得时是 JSON null（protojson 会把 0 输出成 0、缺省字段整个省略），
// 且 int32 必须是 JSON number。
type platformComponentPayload struct {
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	Namespace       string  `json:"namespace"`
	Group           string  `json:"group"`
	Status          string  `json:"status"`
	DesiredReplicas *int32  `json:"desired_replicas"`
	ReadyReplicas   *int32  `json:"ready_replicas"`
	Version         *string `json:"version"`
	ScrapeStatus    *string `json:"scrape_status"`
	Reason          *string `json:"reason"`
}

type platformComponentGroupPayload struct {
	Name       string                     `json:"name"`
	Components []platformComponentPayload `json:"components"`
}

type platformComponentDevProfilePayload struct {
	Mode         string `json:"mode"`
	Provider     string `json:"provider"`
	RealProvider bool   `json:"real_provider"`
	Reason       string `json:"reason"`
}

type platformComponentsPayload struct {
	ObservedAt string                             `json:"observed_at"`
	Groups     []platformComponentGroupPayload    `json:"groups"`
	DevProfile platformComponentDevProfilePayload `json:"dev_profile"`
}

func platformComponentsInvalidHTTP() error {
	return kratoserrors.BadRequest("INVALID_PLATFORM_COMPONENT_REQUEST", "invalid platform component request")
}

// RegisterPlatformComponentHTTP 注册平台组件状态快照的 GET handler（薄封装）：
// 无查询参数、无请求体、无非规范路径，任何多余输入一律 400。
func RegisterPlatformComponentHTTP(server *khttp.Server, s *service.PlatformComponentService) {
	if s == nil {
		return
	}
	server.Route("/").GET(data.PlatformComponentsPath, func(ctx khttp.Context) error {
		khttp.SetOperation(ctx, "/admin.service.v1.PlatformComponentService/ListPlatformComponents")
		r := ctx.Request()
		if r.URL.RawQuery != "" || r.URL.ForceQuery || (r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path) {
			return platformComponentsInvalidHTTP()
		}
		if r.Body != nil {
			if raw, err := io.ReadAll(io.LimitReader(r.Body, 1)); err != nil || len(raw) != 0 {
				return platformComponentsInvalidHTTP()
			}
		}
		var snapshot *data.PlatformComponentSnapshot
		_, err := ctx.Middleware(func(call context.Context, _ any) (any, error) {
			result, err := s.ListPlatformComponents(call)
			snapshot = result
			return result, err
		})(ctx, nil)
		if err != nil {
			return platformComponentHTTPError(err)
		}
		if snapshot == nil {
			return kratoserrors.New(http.StatusInternalServerError, "COMPONENT_STATUS_FAILED", "component status unavailable")
		}
		return writePlatformComponents(ctx, snapshot)
	})
}

// platformComponentHTTPError 只把已声明的 Kratos 错误原样返回，其余统一 500，
// 不把内部错误细节交给调用方。
func platformComponentHTTPError(err error) error {
	var known *kratoserrors.Error
	if errors.As(err, &known) {
		return known
	}
	return kratoserrors.New(http.StatusInternalServerError, "COMPONENT_STATUS_FAILED", "component status unavailable")
}

func writePlatformComponents(ctx khttp.Context, snapshot *data.PlatformComponentSnapshot) error {
	grouped := make(map[string][]platformComponentPayload, len(data.PlatformComponentGroups))
	for _, component := range snapshot.Components {
		grouped[component.Group] = append(grouped[component.Group], platformComponentPayload{
			Name:            component.Name,
			Kind:            component.Kind,
			Namespace:       component.Namespace,
			Group:           component.Group,
			Status:          component.Status,
			DesiredReplicas: component.DesiredReplicas,
			ReadyReplicas:   component.ReadyReplicas,
			Version:         component.Version,
			ScrapeStatus:    component.ScrapeStatus,
			Reason:          component.Reason,
		})
	}
	groups := make([]platformComponentGroupPayload, 0, len(data.PlatformComponentGroups))
	for _, name := range data.PlatformComponentGroups {
		components := grouped[name]
		if components == nil {
			// 契约要求三个组都出现，即使为空。
			components = []platformComponentPayload{}
		}
		groups = append(groups, platformComponentGroupPayload{Name: name, Components: components})
	}
	body := platformComponentsPayload{
		ObservedAt: snapshot.ObservedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Groups:     groups,
		DevProfile: platformComponentDevProfilePayload{
			Mode:         "real",
			Provider:     "kubernetes-component-status",
			RealProvider: snapshot.RealProvider,
			Reason:       snapshot.Reason,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx.Response().Header().Set("Content-Type", "application/json")
	ctx.Response().WriteHeader(http.StatusOK)
	_, err = ctx.Response().Write(raw)
	return err
}
