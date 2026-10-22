package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

// Limit this contract migration to version filtering and operation history.
func modelQueryHTTPFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodGet && len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "models" && (parts[4] == "versions" || parts[4] == "operations") {
			if err := validateModelQuery(r, parts[4] == "versions"); err != nil {
				khttp.DefaultErrorEncoder(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func validateModelQuery(r *http.Request, versions bool) error {
	invalid := func() error {
		return kerrors.BadRequest("INVALID_MODEL_QUERY", "unsupported or invalid model query parameter")
	}
	if len(r.URL.RawQuery) > 4096 || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return invalid()
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid()
	}
	for key, values := range query {
		if len(values) != 1 {
			return invalid()
		}
		value := values[0]
		switch key {
		case "page", "pageSize":
			n, err := strconv.ParseInt(value, 10, 32)
			if err != nil || n < 1 || strconv.FormatInt(n, 10) != value || n > 100 {
				return invalid()
			}
		case "status":
			if !versions {
				return invalid()
			}
			switch value {
			case "pending", "importing", "ready", "error", "downloading", "failed":
			default:
				return invalid()
			}
		default:
			return invalid()
		}
	}
	return nil
}
