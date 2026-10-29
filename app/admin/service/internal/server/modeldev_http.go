package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	nethttp "net/http"
	"strconv"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
)

// The generated request describes the public schema. This route preserves
// typed parameter presence by passing the shared parsed Intent to service.
func registerModelDevHTTP(server *http.Server, modeldev *service.ModelDevService) {
	registerModelDevReadinessHTTP(server, modeldev)
	registerModelDevStopHTTP(server, modeldev)
	registerModelDevQueryHTTP(server, modeldev)
	registerModelDevCatalogueHTTP(server, modeldev)
	registerModelDevRegistrationHTTP(server, modeldev)
	// The stream route is always registered so the authentication chain runs
	// before any channel decision; a missing service still fails closed.
	RegisterExecutionLogStreamHTTP(server, modeldev, DefaultInstanceLogLimits())
	RegisterModelDevArtifactDownloadHTTP(server, modeldev)
	server.Route("/").POST("/api/v1/modeldev/executions", func(ctx http.Context) error {
		http.SetOperation(ctx, adminV1.OperationModelDevServiceCreateExecution)
		request := ctx.Request()
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			return invalidModelDevCreate()
		}
		in, err := decodeModelDevCreate(request.Body)
		if err != nil {
			return err
		}
		handler := ctx.Middleware(func(callCtx context.Context, value interface{}) (interface{}, error) {
			if modeldev == nil {
				return nil, errors.ServiceUnavailable("MODELDEV_CREATE_UNAVAILABLE", "modeldev create unavailable")
			}
			return modeldev.CreateExecution(callCtx, value.(service.ModelDevCreateInput))
		})
		out, err := handler(ctx, in)
		if err != nil {
			return err
		}
		reply, ok := out.(*modeldevv1.CreateExecutionResponse)
		if !ok || reply == nil {
			return errors.ServiceUnavailable("MODELDEV_CREATE_UNAVAILABLE", "modeldev create unavailable")
		}
		return ctx.Result(nethttp.StatusAccepted, reply)
	})
}

func invalidModelDevCreate() error {
	return errors.BadRequest("INVALID_MODELDEV_CREATE", "invalid modeldev create request")
}

func decodeModelDevCreate(reader io.Reader) (service.ModelDevCreateInput, error) {
	invalid := func() (service.ModelDevCreateInput, error) {
		return service.ModelDevCreateInput{}, invalidModelDevCreate()
	}
	if reader == nil {
		return invalid()
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 16385))
	if err != nil || len(raw) > 16384 || !utf8.Valid(raw) {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return invalid()
	}
	seen := make(map[string]bool)
	var intent bytes.Buffer
	intent.WriteByte('{')
	var key string
	for decoder.More() {
		nameToken, err := decoder.Token()
		name, ok := nameToken.(string)
		if err != nil || !ok || seen[name] {
			return invalid()
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return invalid()
		}
		if name == "idempotency_key" {
			if json.Unmarshal(value, &key) != nil || key == "" || !validModelDevKeyEscapes(value) {
				return invalid()
			}
			for _, character := range key {
				if unicode.IsControl(character) {
					return invalid()
				}
			}
			continue
		}
		if intent.Len() > 1 {
			intent.WriteByte(',')
		}
		encodedName, err := json.Marshal(name)
		if err != nil {
			return invalid()
		}
		intent.Write(encodedName)
		intent.WriteByte(':')
		// Preserve nested JSON bytes for the shared parser's duplicate-key,
		// null, Unicode and registered-parameter checks.
		intent.Write(value)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || !seen["idempotency_key"] {
		return invalid()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid()
	}
	intent.WriteByte('}')
	parsed, err := cpup01.ParseIntent(intent.Bytes())
	if err != nil {
		return invalid()
	}
	return service.ModelDevCreateInput{Intent: parsed, IdempotencyKey: key}, nil
}

// encoding/json replaces lone UTF-16 surrogates. A key must not silently
// collapse distinct invalid input bytes onto the same idempotency identity.
// JSON syntax has already been checked; only this extracted string needs the
// escape check. All Intent strings remain the shared parser's responsibility.
func validModelDevKeyEscapes(raw []byte) bool {
	for index := 1; index < len(raw)-1; index++ {
		if raw[index] != '\\' {
			continue
		}
		index++
		if raw[index] != 'u' {
			continue
		}
		if index+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		index += 4
		if !utf16.IsSurrogate(rune(value)) {
			continue
		}
		if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[index+3:index+7]), 16, 16)
		if err != nil || utf16.DecodeRune(rune(value), rune(low)) == unicode.ReplacementChar {
			return false
		}
		index += 6
	}
	return true
}
