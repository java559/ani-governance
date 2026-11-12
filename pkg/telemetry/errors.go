package telemetry

import (
	"context"
	stdErrors "errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Output caps for a single error string. Anything longer is truncated at UTF-8
// boundaries and flagged, so a misbehaving dependency cannot blow up one log
// line while normal RBAC/SQLSTATE/connection errors stay intact.
const (
	errorOutputCap   = 32 * 1024
	errorHeadBytes   = 24 * 1024
	errorTailBytes   = 8 * 1024
	errorTruncatedOn = "[truncated]"
)

// ErrorFields extracts actionable fields from the real cause of err.
//
// It never rewrites or replaces the business error: the caller keeps the
// original value, and only this log copy is redacted and length-capped.
func ErrorFields(err error) []any {
	if err == nil {
		return nil
	}
	message, truncated, originalBytes := safeErrorMessage(err)
	out := []any{
		"error", message,
		"error_type", errorType(err),
		"error_source", errorSource(err),
	}
	if code := errorCode(err); code != "" {
		out = append(out, "error_code", code)
	}
	if truncated {
		out = append(out, "error_truncated", true, "error_original_bytes", originalBytes)
	}
	return out
}

// ErrorAttrs renders ErrorFields as slog attributes.
func ErrorAttrs(err error) []slog.Attr {
	kv := ErrorFields(err)
	if len(kv) == 0 {
		return nil
	}
	out := make([]slog.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		switch v := kv[i+1].(type) {
		case string:
			out = append(out, slog.String(key, v))
		case bool:
			out = append(out, slog.Bool(key, v))
		case int:
			out = append(out, slog.Int(key, v))
		}
	}
	return out
}

// errorType returns the concrete provider/driver type name, falling back to the
// Go type of the outermost error.
func errorType(err error) string {
	var (
		k8sErr *apierrors.StatusError
		pgErr  *pgconn.PgError
		netErr net.Error
		urlErr *url.Error
		kerr   *kerrors.Error
	)
	switch {
	case stdErrors.As(err, &k8sErr):
		return "StatusError"
	case stdErrors.As(err, &pgErr):
		return "PgError"
	// A Kratos error implements GRPCStatus(); classify it as the local business
	// error it is rather than as an upstream gRPC dependency failure.
	case stdErrors.As(err, &kerr):
		return "kratos_error"
	case isGRPCStatus(err):
		return "grpc_status"
	case stdErrors.As(err, &urlErr):
		return "url.Error"
	case stdErrors.As(err, &netErr):
		return "net.Error"
	}
	return typeName(err)
}

// errorSource classifies where the failure came from. Only real typed evidence
// is used; unknown errors stay "local" rather than guessing a dependency.
//
// A dependency error wrapped inside a Kratos error keeps its real source: the
// cause is inspected before the outer business envelope is considered.
func errorSource(err error) string {
	var (
		k8sErr *apierrors.StatusError
		pgErr  *pgconn.PgError
		urlErr *url.Error
	)
	switch {
	case stdErrors.As(err, &k8sErr):
		return "kubernetes"
	case stdErrors.As(err, &pgErr):
		return "postgresql"
	case stdErrors.As(err, &urlErr):
		return "http"
	}
	var kerr *kerrors.Error
	if stdErrors.As(err, &kerr) {
		// The outer envelope is a business error; if it wraps a real transport
		// cause, the grpc classification below still applies.
		if cause := stdErrors.Unwrap(kerr); cause != nil && isGRPCStatus(cause) {
			return "grpc"
		}
		return "local"
	}
	if isGRPCStatus(err) {
		return "grpc"
	}
	return "local"
}

// errorCode returns a stable machine-readable code when the provider supplies
// one: Kubernetes Status Reason, SQLSTATE, or gRPC code name.
func errorCode(err error) string {
	var (
		k8sErr *apierrors.StatusError
		pgErr  *pgconn.PgError
	)
	switch {
	case stdErrors.As(err, &k8sErr):
		if reason := string(k8sErr.ErrStatus.Reason); reason != "" {
			return reason
		}
		if k8sErr.ErrStatus.Code != 0 {
			return httpStatusCodeName(int(k8sErr.ErrStatus.Code))
		}
	case stdErrors.As(err, &pgErr):
		return pgErr.Code
	}
	// A Kratos error is also a gRPC status; its stable business reason is the
	// more actionable code, so it wins over the transport code name.
	var ke *kerrors.Error
	if stdErrors.As(err, &ke) && ke.Reason != "" {
		return ke.Reason
	}
	if st, ok := status.FromError(err); ok && st.Code().String() != "OK" {
		return st.Code().String()
	}
	return ""
}

// isGRPCStatus reports whether err carries a non-OK gRPC status.
func isGRPCStatus(err error) bool {
	st, ok := status.FromError(err)
	return ok && st.Code().String() != "OK"
}

// safeErrorMessage renders the real error text, redacts known credential
// shapes and caps the length at UTF-8 boundaries.
func safeErrorMessage(err error) (string, bool, int) {
	raw := Redact(err.Error())
	size := len(raw)
	if size <= errorOutputCap {
		return raw, false, size
	}
	head := trimToRuneBoundary(raw[:errorHeadBytes], false)
	tail := trimToRuneBoundary(raw[size-errorTailBytes:], true)
	return head + errorTruncatedOn + tail, true, size
}

// trimToRuneBoundary drops partial runes at a cut edge.
func trimToRuneBoundary(s string, fromStart bool) string {
	if utf8.ValidString(s) {
		return s
	}
	if fromStart {
		for len(s) > 0 {
			r, size := utf8.DecodeRuneInString(s)
			if r != utf8.RuneError || size > 1 {
				return s
			}
			s = s[size:]
		}
		return s
	}
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size > 1 {
			return s
		}
		s = s[:len(s)-size]
	}
	return s
}

var (
	// key=value / key: value pairs whose value must never reach the log.
	credentialPair = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|secret[_-]?key|private[_-]?key|authorization|cookie|credential|client[_-]?secret)\b(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&}\]"']+)`)
	// scheme://user:pass@host
	urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)
	// Bearer <token>
	bearerToken = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}`)
	// PEM private key blocks
	pemKey = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
)

// Redact replaces only known credential shapes. The rest of the message keeps
// the call identity, resource coordinates and provider reason so an operator
// can still act on it.
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = pemKey.ReplaceAllString(s, "[redacted private key]")
	s = urlUserinfo.ReplaceAllString(s, "$1[redacted]@")
	s = bearerToken.ReplaceAllString(s, "$1 [redacted]")
	s = credentialPair.ReplaceAllStringFunc(s, func(match string) string {
		loc := credentialPair.FindStringSubmatchIndex(match)
		if loc == nil {
			return match
		}
		key := match[loc[2]:loc[3]]
		sep := match[loc[4]:loc[5]]
		return key + sep + "[redacted]"
	})
	return s
}

func typeName(err error) string {
	if err == nil {
		return ""
	}
	name := strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return "error"
	}
	return name
}

// IsDeadlineOrCanceled reports whether the failure is the caller's own deadline
// or cancellation. Callers use it to decide informational vs error levels.
func IsDeadlineOrCanceled(err error) bool {
	return stdErrors.Is(err, context.Canceled) || stdErrors.Is(err, context.DeadlineExceeded)
}

// httpStatusCodeName maps a Kubernetes Status Code to its phrase.
func httpStatusCodeName(code int) string {
	switch code {
	case 400:
		return "BadRequest"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "NotFound"
	case 405:
		return "MethodNotAllowed"
	case 409:
		return "Conflict"
	case 422:
		return "UnprocessableEntity"
	case 429:
		return "TooManyRequests"
	case 500:
		return "InternalError"
	case 503:
		return "ServiceUnavailable"
	case 504:
		return "Timeout"
	default:
		return ""
	}
}
