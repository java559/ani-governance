package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

func acceleratorInitializationHTTPFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		list := request.Method == http.MethodGet && request.URL.Path == "/api/v1/admin/accelerator/discovered-nodes"
		read := request.Method == http.MethodGet && request.URL.Path == "/api/v1/admin/accelerator/initialization"
		create := request.Method == http.MethodPost && request.URL.Path == "/api/v1/admin/accelerator/initializations"
		operationPath := strings.TrimPrefix(request.URL.Path, "/api/v1/admin/accelerator/initializations/")
		operation := operationPath != request.URL.Path && operationPath != "" && !strings.Contains(operationPath, "/") && (request.Method == http.MethodGet || request.Method == http.MethodPost && strings.HasSuffix(operationPath, ":retry"))
		if !list && !read && !create && !operation {
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		if !validAcceleratorInitializationQuery(request, list) {
			khttp.DefaultErrorEncoder(writer, request, errors.BadRequest("INVALID_ACCELERATOR_REQUEST", "invalid initialization query"))
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func validAcceleratorInitializationQuery(request *http.Request, list bool) bool {
	if request.URL.ForceQuery || len(request.URL.RawQuery) > 4096 {
		return false
	}
	if !list {
		return request.URL.RawQuery == ""
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range query {
		if key != "page" && key != "pageSize" || len(values) != 1 {
			return false
		}
		value, err := strconv.ParseUint(values[0], 10, 32)
		if err != nil || value == 0 || values[0] != strconv.FormatUint(value, 10) || key == "pageSize" && value > 200 {
			return false
		}
	}
	return true
}
