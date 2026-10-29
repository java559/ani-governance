package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
)

func registerModelDevReadinessHTTP(server *khttp.Server, modeldev *service.ModelDevService) {
	server.Route("/").POST(data.ModelDevReadinessPath, func(ctx khttp.Context) error {
		modelDevQueryHeaders(ctx)
		khttp.SetOperation(ctx, adminv1.OperationModelDevServiceCheckFinetuningReadiness)
		request := ctx.Request()
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			return invalidModelDevReadiness()
		}
		in, err := decodeModelDevReadiness(request.Body)
		if err != nil {
			return err
		}
		handler := ctx.Middleware(func(call context.Context, value interface{}) (interface{}, error) {
			if modeldev == nil {
				return nil, errors.ServiceUnavailable("MODELDEV_QUERY_UNAVAILABLE", "modeldev readiness unavailable")
			}
			return modeldev.CheckFinetuningReadiness(call, value.(*modeldevv1.CheckFinetuningReadinessRequest))
		})
		out, err := handler(ctx, in)
		if err != nil {
			return err
		}
		return ctx.Result(http.StatusOK, out)
	})
}

func invalidModelDevReadiness() error {
	return errors.BadRequest("INVALID_MODELDEV_QUERY", "invalid finetuning readiness request")
}

func decodeModelDevReadiness(reader io.Reader) (*modeldevv1.CheckFinetuningReadinessRequest, error) {
	if reader == nil {
		return nil, invalidModelDevReadiness()
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 16385))
	if err != nil || len(raw) > 16384 || !utf8.Valid(raw) {
		return nil, invalidModelDevReadiness()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, invalidModelDevReadiness()
	}
	in := new(modeldevv1.CheckFinetuningReadinessRequest)
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return nil, invalidModelDevReadiness()
		}
		seen[name] = true
		var encoded json.RawMessage
		var value string
		if decoder.Decode(&encoded) != nil || bytes.Equal(encoded, []byte("null")) || json.Unmarshal(encoded, &value) != nil || value == "" || !validModelDevKeyEscapes(encoded) {
			return nil, invalidModelDevReadiness()
		}
		switch name {
		case "base_model_version_id":
			in.BaseModelVersionId = value
		case "compute_preset_id":
			in.ComputePresetId = value
		case "finetuning_template":
			in.FinetuningTemplate = &value
		default:
			return nil, invalidModelDevReadiness()
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || !seen["base_model_version_id"] || !seen["compute_preset_id"] {
		return nil, invalidModelDevReadiness()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalidModelDevReadiness()
	}
	return in, nil
}
