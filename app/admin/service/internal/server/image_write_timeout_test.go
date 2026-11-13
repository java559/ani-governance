package server

import "testing"

// 注销的真实路径末段是 "img_xxx:unregister"，不是占满整段的参数，因此
// 必须靠"参数 + 固定后缀"匹配。这里同时固定"未知后缀不命中"，避免把普通
// 请求错误地提到镜像写预算。
func TestImageWriteRouteMatchCoversUnregister(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"平台注销", "POST", "/api/v1/platform/images/registrations/img_0123456789abcdef0123456789abcdef:unregister", true},
		{"代管注销", "POST", "/api/v1/platform/tenants/2/images/registrations/img_0123456789abcdef0123456789abcdef:unregister", true},
		{"旧租户注销", "POST", "/api/v1/images/registrations/img_0123456789abcdef0123456789abcdef:unregister", true},
		{"平台 PATCH", "PATCH", "/api/v1/platform/images/registrations/img_0123456789abcdef0123456789abcdef", true},
		{"平台登记", "POST", "/api/v1/platform/images/registrations", true},
		{"空格启用", "POST", "/api/v1/platform/images/space:enable", true},
		{"平台列表 GET 不命中", "GET", "/api/v1/platform/images/registrations", false},
		{"未知后缀不命中", "POST", "/api/v1/platform/images/registrations/img_0123456789abcdef0123456789abcdef:publish", false},
		{"缺 id 不命中", "POST", "/api/v1/platform/images/registrations/:unregister", false},
		{"其他业务不命中", "POST", "/api/v1/instances", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := imageWriteRouteMatch(c.method, c.path); got != c.want {
				t.Fatalf("%s %s = %v, want %v", c.method, c.path, got, c.want)
			}
		})
	}
}
