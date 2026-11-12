package server

import (
	"errors"
	"fmt"
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
	return decodeStrictJSONHTTPWithLimit(request, message, 65536)
}

func decodeStrictJSONHTTPWithLimit(request *http.Request, message proto.Message, limit int64) (err error) {
	// Callers keep their own public error; the request capture records the real
	// parse cause so the completion log does not stop at a safe status text.
	detail := errors.New("invalid JSON request")
	defer func() {
		if err == nil || request == nil {
			return
		}
		captureHTTPError(request.Context(), detail)
	}()
	if request == nil || request.Body == nil || request.URL == nil || request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.RawPath != "" && request.URL.RawPath != request.URL.Path {
		detail = errors.New("invalid JSON request envelope")
		return errInvalidJSONRequest
	}
	media, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		detail = errors.New("content type must be application/json")
		return errInvalidJSONRequest
	}
	for _, header := range []string{"X-Ani-Tenant-Id", "X-Tenant-Id", "X-Ani-Actor", "X-Ani-Operator", "X-Resource-Tenant-Id"} {
		if len(request.Header.Values(header)) != 0 {
			detail = fmt.Errorf("untrusted identity header %s is not allowed", header)
			return errInvalidJSONRequest
		}
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		detail = fmt.Errorf("request body must be non-empty and at most %d bytes", limit)
		if err != nil {
			detail = fmt.Errorf("cannot read request body: %w", err)
		}
		return errInvalidJSONRequest
	}
	if err = (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, message); err != nil {
		detail = fmt.Errorf("invalid JSON body: %w", err)
		return errInvalidJSONRequest
	}
	return nil
}
