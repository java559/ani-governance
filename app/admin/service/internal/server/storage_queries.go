package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

// storageQueryHTTPFilter 在生成代码解码前执行严格查询校验。
// kratos form 解码会忽略空值、静默丢弃未知字段并接受前导零，与
// docs/contracts/frontend-http-api.md 的分页合同不一致，因此在这里统一拒绝。
func storageQueryHTTPFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || !isStoragePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if err := validateStorageQuery(r); err != nil {
			khttp.DefaultErrorEncoder(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isStoragePath(path string) bool {
	return path == "/api/v1/storage" || strings.HasPrefix(path, "/api/v1/storage/")
}

func storageInvalidQuery() error {
	return kerrors.BadRequest("STORAGE_INVALID_REQUEST", "存储请求参数不合法")
}

// validateStorageQuery 只允许四个列表 GET 携带声明过的查询参数；
// 空值、前导零、重复参数、未知参数以及其他 storage 路由上的查询串一律 400。
func validateStorageQuery(r *http.Request) error {
	// GET 列表不接受请求体；写接口的 JSON body 不在这里校验。
	if len(r.URL.RawQuery) > 4096 || r.URL.RawPath != "" || r.URL.ForceQuery || (r.Method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) != 0)) {
		return storageInvalidQuery()
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return storageInvalidQuery()
	}
	fields, ok := storageQueryFields(r.Method, r.URL.Path)
	if !ok {
		if len(query) != 0 || r.URL.RawQuery != "" {
			return storageInvalidQuery()
		}
		return nil
	}
	for key, values := range query {
		check, declared := fields[key]
		if !declared || len(values) != 1 || values[0] == "" {
			return storageInvalidQuery()
		}
		if err := check(values[0]); err != nil {
			return err
		}
	}
	return nil
}

var storageGetQueryFields = map[string]map[string]func(string) error{
	"/api/v1/storage/classes": {
		"page":     canonicalPage,
		"pageSize": canonicalPageSize,
	},
	"/api/v1/storage/volumes": {
		"page":     canonicalPage,
		"pageSize": canonicalPageSize,
		"name":     filterName,
		"id":       filterID(253),
		"state":    volumeFilterState,
	},
	"/api/v1/storage/buckets": {
		"page":     canonicalPage,
		"pageSize": canonicalPageSize,
		"name":     filterName,
		"id":       filterID(64),
	},
}

var storageObjectsQueryFields = map[string]func(string) error{
	"prefix": objectPrefix,
	"limit":  objectLimit,
	"cursor": objectCursor,
}

// storageQueryFields 返回路由允许的查询参数；第四个参数为 false 表示该路由不接受查询串。
func storageQueryFields(method, path string) (map[string]func(string) error, bool) {
	if method != http.MethodGet {
		return nil, false
	}
	if fields, ok := storageGetQueryFields[path]; ok {
		return fields, true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "storage" && parts[3] == "buckets" && parts[5] == "objects" {
		return storageObjectsQueryFields, true
	}
	return nil, false
}

func canonicalPage(value string) error {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != value {
		return storageInvalidQuery()
	}
	return nil
}

func canonicalPageSize(value string) error {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n == 0 || n > 100 || strconv.FormatUint(n, 10) != value {
		return storageInvalidQuery()
	}
	return nil
}

func objectLimit(value string) error {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n == 0 || n > 100 || strconv.FormatUint(n, 10) != value {
		return storageInvalidQuery()
	}
	return nil
}

func filterName(value string) error {
	name := strings.TrimSpace(value)
	if name == "" || utf8.RuneCountInString(name) > 128 {
		return storageInvalidQuery()
	}
	return nil
}

func filterID(maxLength int) func(string) error {
	return func(value string) error {
		id := strings.TrimSpace(value)
		if id == "" || len(id) > maxLength {
			return storageInvalidQuery()
		}
		return nil
	}
}

func volumeFilterState(value string) error {
	switch value {
	case "Pending", "Bound", "Lost":
		return nil
	default:
		return storageInvalidQuery()
	}
}

func objectPrefix(value string) error {
	if len(value) > 1024 || strings.ContainsRune(value, 0) {
		return storageInvalidQuery()
	}
	return nil
}

func objectCursor(value string) error {
	if len(value) > 4096 {
		return storageInvalidQuery()
	}
	return nil
}
