package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/types/known/emptypb"
)

// 预置状态键与 service 层一致：响应必须给出完整对象，不能因空租户变成 null。
var (
	overviewInstanceStateKeys   = []string{"pending", "running", "stopped", "failed"}
	overviewInferenceStatusKeys = []string{"pending", "deploying", "running", "stopping", "stopped", "failed"}
	overviewModelStatusKeys     = []string{"pending", "importing", "ready", "error"}
	overviewKnowledgeBaseKeys   = []string{"active", "rebuilding"}
)

type overviewInstancePayload struct {
	Total   int64            `json:"total"`
	ByState map[string]int64 `json:"by_state"`
	Other   int64            `json:"other"`
}

type overviewStatusPayload struct {
	Total    int64            `json:"total"`
	ByStatus map[string]int64 `json:"by_status"`
	Other    int64            `json:"other"`
}

// overviewPayload 用 encoding/json 输出计数：protojson 会把 int64 与 map 值编码成
// 字符串，而本接口契约要求 JSON number（前端直接参与算术）。
type overviewPayload struct {
	Instances         overviewInstancePayload `json:"instances"`
	InferenceServices overviewStatusPayload   `json:"inference_services"`
	Models            overviewStatusPayload   `json:"models"`
	KnowledgeBases    overviewStatusPayload   `json:"knowledge_bases"`
}

func overviewInvalidHTTP() error {
	return kratoserrors.BadRequest("INVALID_OVERVIEW_REQUEST", "invalid overview request")
}

// RegisterOverviewHTTP 注册首页资源统计卡片的 GET handler（薄封装）：
// 无查询参数、无请求体、无非规范路径，任何多余输入一律 400。
func RegisterOverviewHTTP(server *khttp.Server, s *service.OverviewService) {
	server.Route("/").GET(data.OverviewPath, func(ctx khttp.Context) error {
		khttp.SetOperation(ctx, admin.OperationOverviewServiceGetOverview)
		r := ctx.Request()
		if r.URL.RawQuery != "" || r.URL.ForceQuery || (r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path) {
			return overviewInvalidHTTP()
		}
		if r.Body != nil {
			if raw, err := io.ReadAll(io.LimitReader(r.Body, 1)); err != nil || len(raw) != 0 {
				return overviewInvalidHTTP()
			}
		}
		out, err := ctx.Middleware(func(call context.Context, _ any) (any, error) {
			return s.GetOverview(call, &emptypb.Empty{})
		})(ctx, &emptypb.Empty{})
		if err != nil {
			return instanceHTTPError(err)
		}
		reply, ok := out.(*admin.ConsoleOverviewResponse)
		if !ok || reply == nil {
			return kratoserrors.New(http.StatusInternalServerError, "OVERVIEW_INTERNAL", "overview unavailable")
		}
		return writeOverviewResult(ctx, reply)
	})
}

func writeOverviewResult(ctx khttp.Context, reply *admin.ConsoleOverviewResponse) error {
	instances := reply.GetInstances()
	body := overviewPayload{
		Instances: overviewInstancePayload{
			Total:   instances.GetTotal(),
			ByState: overviewCounts(instances.GetByState(), overviewInstanceStateKeys),
			Other:   instances.GetOther(),
		},
		InferenceServices: overviewStatusPayload{
			Total:    reply.GetInferenceServices().GetTotal(),
			ByStatus: overviewCounts(reply.GetInferenceServices().GetByStatus(), overviewInferenceStatusKeys),
			Other:    reply.GetInferenceServices().GetOther(),
		},
		Models: overviewStatusPayload{
			Total:    reply.GetModels().GetTotal(),
			ByStatus: overviewCounts(reply.GetModels().GetByStatus(), overviewModelStatusKeys),
			Other:    reply.GetModels().GetOther(),
		},
		KnowledgeBases: overviewStatusPayload{
			Total:    reply.GetKnowledgeBases().GetTotal(),
			ByStatus: overviewCounts(reply.GetKnowledgeBases().GetByStatus(), overviewKnowledgeBaseKeys),
			Other:    reply.GetKnowledgeBases().GetOther(),
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

// overviewCounts 保证预置键齐全且为 number；缺失键补 0，多余键保留。
func overviewCounts(in map[string]int64, keys []string) map[string]int64 {
	out := make(map[string]int64, len(in)+len(keys))
	for key, count := range in {
		out[key] = count
	}
	for _, key := range keys {
		if _, ok := out[key]; !ok {
			out[key] = 0
		}
	}
	return out
}
