package server

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var errInvalidJSONRequest = errors.New("invalid JSON request")

// Public command decoders reject unknown fields and caller-supplied internal
// identity. Each business boundary maps this error to its own public reason.
func decodeStrictJSONHTTP(request *http.Request, message proto.Message) error {
	if request == nil || request.Body == nil || request.URL == nil || request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.RawPath != "" && request.URL.RawPath != request.URL.Path {
		return errInvalidJSONRequest
	}
	media, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errInvalidJSONRequest
	}
	for _, header := range []string{"X-Ani-Tenant-Id", "X-Tenant-Id", "X-Ani-Actor", "X-Ani-Operator", "X-Resource-Tenant-Id"} {
		if len(request.Header.Values(header)) != 0 {
			return errInvalidJSONRequest
		}
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, 65537))
	if err != nil || len(raw) == 0 || len(raw) > 65536 {
		return errInvalidJSONRequest
	}
	if err = (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, message); err != nil {
		return errInvalidJSONRequest
	}
	return nil
}
