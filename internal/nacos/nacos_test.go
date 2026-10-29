package nacos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeAddr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"127.0.0.1:8848", "http://127.0.0.1:8848/nacos"},
		{"http://10.0.0.2:8848/nacos/", "http://10.0.0.2:8848/nacos"},
		{"nacos.local", "http://nacos.local:8848/nacos"},
		{"https://nacos.local:9443/custom", "https://nacos.local:9443/custom"},
		{"  127.0.0.1:8848  ", "http://127.0.0.1:8848/nacos"},
	}
	for _, c := range cases {
		got, err := NormalizeAddr(c.in)
		if err != nil {
			t.Fatalf("NormalizeAddr(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("NormalizeAddr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "   ", "ftp://host:21", "://"} {
		if _, err := NormalizeAddr(bad); err == nil {
			t.Errorf("NormalizeAddr(%q) 期望报错，实际通过", bad)
		}
	}
}

func TestHeadersRoundTrip(t *testing.T) {
	h := http.Header{}
	in := Config{Addr: "http://127.0.0.1:8848/nacos", Username: "nacos", Password: "密码123", Namespace: "开发环境", Insecure: true}
	SetHeaders(h, in)
	// 头值必须是 ASCII，否则会被 HTTP 层拒绝
	for k, vs := range h {
		for _, v := range vs {
			for _, r := range v {
				if r > 127 {
					t.Fatalf("请求头 %s 含非 ASCII 字符：%q", k, v)
				}
			}
		}
	}
	out := ConfigFromHeaders(h)
	if out != in {
		t.Fatalf("往返不一致：got %+v, want %+v", out, in)
	}
}

func fakeNacos(t *testing.T, withCatalog bool) *httptest.Server {
	t.Helper()
	guard := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("accessToken") != "tk" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":403,"message":"user not found!"}`))
				return
			}
			next(w, r)
		}
	}
	write := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/nacos/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("password") != "pass" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":403,"message":"unknown user!"}`))
			return
		}
		write(w, `{"accessToken":"tk","tokenTtl":18000}`)
	})
	mux.HandleFunc("/nacos/v1/console/health/readiness", guard(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}))
	mux.HandleFunc("/nacos/v1/console/server/state", guard(func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"version":"2.3.2","server_env":"","datasource_platform":"mysql","auth_enabled":"true","startup_mode":"standalone"}`)
	}))
	mux.HandleFunc("/nacos/v1/ns/operator/metrics", guard(func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"serviceCount":12,"instanceCount":30,"cpu":0.05,"load":0.3,"mem":0.4}`)
	}))
	mux.HandleFunc("/nacos/v1/console/namespaces", guard(func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"code":200,"data":[{"namespace":"","namespaceShowName":"public","quota":200,"configCount":3,"type":0}]}`)
	}))
	if withCatalog {
		mux.HandleFunc("/nacos/v1/ns/catalog/services", guard(func(w http.ResponseWriter, r *http.Request) {
			write(w, `{"count":1,"serviceList":[{"name":"order-service","groupName":"DEFAULT_GROUP","clusterCount":1,"ipCount":2,"healthyCount":1,"triggerFlag":false}]}`)
		}))
	} else {
		mux.HandleFunc("/nacos/v1/ns/catalog/services", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found"))
		})
		mux.HandleFunc("/nacos/v1/ns/service/list", guard(func(w http.ResponseWriter, r *http.Request) {
			write(w, `{"count":1,"doms":["legacy-service"]}`)
		}))
	}
	mux.HandleFunc("/nacos/v1/ns/instance/list", guard(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("serviceName") != "order-service" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		write(w, `{"name":"DEFAULT_GROUP@@order-service","clusters":"","hosts":[{"ip":"10.0.0.1","port":8080,"healthy":true,"enabled":true,"ephemeral":true,"weight":1,"clusterName":"DEFAULT","metadata":{"version":"1.0"}}]}`)
	}))
	mux.HandleFunc("/nacos/v1/cs/configs", guard(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			_ = r.ParseForm()
			if r.Form.Get("dataId") == "" || r.Form.Get("content") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte("true"))
		case http.MethodDelete:
			_, _ = w.Write([]byte("true"))
		default:
			if r.URL.Query().Get("search") != "" {
				write(w, `{"totalCount":1,"pageItems":[{"id":"1","dataId":"app.yaml","group":"DEFAULT_GROUP","type":"yaml","appName":"demo","md5":"abc"}]}`)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("server:\n  port: 8080\n"))
		}
	}))
	return httptest.NewServer(mux)
}

func TestClient(t *testing.T) {
	srv := fakeNacos(t, true)
	defer srv.Close()

	cl, err := New(Config{Addr: srv.URL, Username: "nacos", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if err := cl.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	st, err := cl.State(ctx)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if st.Version != "2.3.2" || !st.AuthEnabled || st.Datasource != "mysql" {
		t.Fatalf("State 解析异常: %+v", st)
	}
	m, err := cl.Metrics(ctx)
	if err != nil || m.ServiceCount != 12 || m.InstanceCount != 30 {
		t.Fatalf("Metrics: %+v err=%v", m, err)
	}
	nss, err := cl.Namespaces(ctx)
	if err != nil || len(nss) != 1 || nss[0].NamespaceShowName != "public" {
		t.Fatalf("Namespaces: %+v err=%v", nss, err)
	}
	svcList, err := cl.Services(ctx, "", "", "", 1, 20)
	if err != nil || svcList.Total != 1 || len(svcList.Items) != 1 || svcList.Items[0].IPCount != 2 || svcList.Items[0].HealthyCount != 1 {
		t.Fatalf("Services: %+v total=%d err=%v", svcList.Items, svcList.Total, err)
	}
	il, err := cl.Instances(ctx, "", "DEFAULT_GROUP", "order-service", false)
	if err != nil || len(il.Items) != 1 || il.Items[0].IP != "10.0.0.1" || il.Items[0].Port != 8080 {
		t.Fatalf("Instances: %+v err=%v", il, err)
	}
	cfgs, total, err := cl.Configs(ctx, "", "", "app", 1, 20)
	if err != nil || total != 1 || len(cfgs) != 1 || cfgs[0].DataID != "app.yaml" {
		t.Fatalf("Configs: %+v total=%d err=%v", cfgs, total, err)
	}
	content, err := cl.ConfigContent(ctx, "", "DEFAULT_GROUP", "app.yaml")
	if err != nil || !strings.Contains(content, "port: 8080") {
		t.Fatalf("ConfigContent: %q err=%v", content, err)
	}
	if err := cl.PublishConfig(ctx, "", "DEFAULT_GROUP", "app.yaml", "a: 1", "yaml"); err != nil {
		t.Fatalf("PublishConfig: %v", err)
	}
	if err := cl.DeleteConfig(ctx, "", "DEFAULT_GROUP", "app.yaml"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
}

func TestServicesFallback(t *testing.T) {
	srv := fakeNacos(t, false)
	defer srv.Close()

	cl, err := New(Config{Addr: srv.URL, Username: "nacos", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svcList, err := cl.Services(context.Background(), "", "DEFAULT_GROUP", "", 1, 20)
	if err != nil {
		t.Fatalf("Services 兜底失败: %v", err)
	}
	if svcList.Total != 1 || len(svcList.Items) != 1 || svcList.Items[0].Name != "legacy-service" {
		t.Fatalf("兜底结果异常: %+v total=%d", svcList.Items, svcList.Total)
	}
}

// TestInstancesTypeTolerance 同一台服务端既然把 triggerFlag 返回成字符串，
// 实例字段也很可能是字符串/数字混着来。这里全部用字符串喂一遍。
func TestInstancesTypeTolerance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nacos/v1/auth/login":
			_, _ = w.Write([]byte(`{"accessToken":"tk","tokenTtl":18000}`))
		case "/nacos/v1/ns/instance/list":
			_, _ = w.Write([]byte(`{"name":"DEFAULT_GROUP@@ybs-gateway","clusters":"",` +
				`"reachProtectionThreshold":"false","triggerFlag":"false","hosts":[` +
				`{"ip":"10.0.0.1","port":"8080","healthy":"true","enabled":"true","ephemeral":"true",` +
				`"weight":"1.5","clusterName":"DEFAULT","metadata":{"version":"1.0","port":8080}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cl, err := New(Config{Addr: srv.URL, Username: "nacos", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	il, err := cl.Instances(context.Background(), "", "DEFAULT_GROUP", "ybs-gateway", false)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(il.Items) != 1 {
		t.Fatalf("期望 1 个实例，实际 %d", len(il.Items))
	}
	it := il.Items[0]
	if it.IP != "10.0.0.1" || it.Port != 8080 || !it.Healthy || !it.Enabled || !it.Ephemeral {
		t.Fatalf("实例字段解析异常: %+v", it)
	}
	if it.Weight != 1.5 {
		t.Fatalf("weight 解析异常: %v", it.Weight)
	}
	if il.TriggerFlag || il.ProtectThres {
		t.Fatalf("字符串 \"false\" 应解析为 false: trigger=%v protect=%v", il.TriggerFlag, il.ProtectThres)
	}
	if it.Metadata["version"] != "1.0" || it.Metadata["port"] != "8080" {
		t.Fatalf("metadata 解析异常: %+v", it.Metadata)
	}
}

// TestServicesGroupFilterParams 分组过滤的键名是 groupNameParam（Nacos 控制台就这么发），
// 之前只发 groupName，服务端直接忽略 → 按分组过滤没效果。
func TestServicesGroupFilterParams(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nacos/v1/auth/login":
			_, _ = w.Write([]byte(`{"accessToken":"tk","tokenTtl":18000}`))
		case "/nacos/v1/ns/catalog/services":
			got = r.URL.Query()
			// 按 groupNameParam 过滤，模拟服务端真实行为
			g := r.URL.Query().Get("groupNameParam")
			if g != "" && g != "test" {
				_, _ = w.Write([]byte(`{"count":0,"serviceList":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"count":1,"serviceList":[{"name":"ybs-gateway","groupName":"test","ipCount":2,"healthyInstanceCount":2,"triggerFlag":"false"}]}`))
		case "/nacos/v1/ns/service/list":
			_, _ = w.Write([]byte(`{"count":0,"doms":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cl, err := New(Config{Addr: srv.URL, Username: "nacos", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// 带分组过滤：两个键都要发
	if _, err := cl.Services(ctx, "test", "test", "", 1, 20); err != nil {
		t.Fatalf("Services: %v", err)
	}
	if got.Get("groupNameParam") != "test" {
		t.Fatalf("缺少 groupNameParam: %v", got)
	}
	if got.Get("groupName") != "test" {
		t.Fatalf("缺少 groupName 兜底键: %v", got)
	}
	if got.Get("hasIpCount") != "true" {
		t.Fatalf("缺少 hasIpCount: %v", got)
	}

	// 不过滤时一个都不该出现，否则部分版本会当成字面量过滤条件
	got = nil
	if _, err := cl.Services(ctx, "test", "", "", 1, 20); err != nil {
		t.Fatalf("Services: %v", err)
	}
	if got.Get("groupNameParam") != "" || got.Get("groupName") != "" {
		t.Fatalf("未填分组时不该发分组参数: %v", got)
	}
}

// TestServicesRealPayload 用户环境抓到的真实返回：triggerFlag 是字符串 "false"、
// 健康实例数的键名是 healthyInstanceCount。
// 当初 triggerFlag 写成 bool，json.Unmarshal 直接报类型错误，整个 serviceList 数组解析失败，
// 于是接口有 4 个服务、页面却一条都不显示。
func TestServicesRealPayload(t *testing.T) {
	body := `{"count":2,"serviceList":[` +
		`{"name":"ybs-gateway","groupName":"dev","clusterCount":1,"ipCount":2,"healthyInstanceCount":2,"triggerFlag":"false"},` +
		`{"name":"ybs-system","groupName":"dev","clusterCount":1,"ipCount":1,"healthyInstanceCount":1,"triggerFlag":"false"}]}`
	list, ok := parseServiceList([]byte(body))
	if !ok {
		t.Fatal("真实返回体解析失败")
	}
	if len(list.Items) != 2 || list.Total != 2 {
		t.Fatalf("期望 2 条，实际 %d / total=%d", len(list.Items), list.Total)
	}
	first := list.Items[0]
	if first.Name != "ybs-gateway" || first.GroupName != "dev" {
		t.Fatalf("服务名/分组解析异常: %+v", first)
	}
	if first.IPCount != 2 || first.HealthyCount != 2 || first.ClusterCount != 1 {
		t.Fatalf("计数解析异常: cluster=%d ip=%d healthy=%d", first.ClusterCount, first.IPCount, first.HealthyCount)
	}
	if first.TriggerFlag {
		t.Fatal(`triggerFlag="false" 应解析为 false`)
	}
}

// TestServicesNoFallbackOnRealShape 复现用户场景：catalog 有数据但 service/list 返回 0 条，
// 此时必须直接用 catalog 的结果，不能回落。
func TestServicesNoFallbackOnRealShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nacos/v1/auth/login":
			_, _ = w.Write([]byte(`{"accessToken":"tk","tokenTtl":18000}`))
		case "/nacos/v1/ns/catalog/services":
			_, _ = w.Write([]byte(`{"count":2,"serviceList":[` +
				`{"name":"ybs-gateway","groupName":"dev","clusterCount":1,"ipCount":2,"healthyInstanceCount":2,"triggerFlag":"false"},` +
				`{"name":"ybs-system","groupName":"dev","clusterCount":1,"ipCount":1,"healthyInstanceCount":1,"triggerFlag":"false"}]}`))
		case "/nacos/v1/ns/service/list":
			_, _ = w.Write([]byte(`{"count":0,"doms":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cl, err := New(Config{Addr: srv.URL, Username: "nacos", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	list, err := cl.Services(context.Background(), "", "", "", 1, 20)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if list.Source != "catalog" {
		t.Fatalf("catalog 有数据时应直接用它，实际 source=%q", list.Source)
	}
	if len(list.Items) != 2 || list.Items[0].Name != "ybs-gateway" {
		t.Fatalf("结果异常: %+v", list.Items)
	}
	if list.Hint != "" {
		t.Fatalf("有数据时不该带 hint: %q", list.Hint)
	}
}

// TestParseServiceListShapes 覆盖各版本服务列表的返回形状差异。
// 重点是 {code,message,data} 信封——不剥掉的话页面会一直空，但接口明明有返回。
func TestParseServiceListShapes(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		want   int
		source string
	}{
		{"2.x catalog", `{"count":2,"serviceList":[{"name":"a"},{"name":"b"}]}`, 2, "serviceList"},
		{"1.x catalog", `{"totalCount":1,"pageItems":[{"name":"a","ipCount":3}]}`, 1, "pageItems"},
		{"data 信封", `{"code":0,"message":null,"data":{"count":1,"serviceList":[{"name":"a"}]}}`, 1, "serviceList"},
		{"service/list doms", `{"count":1,"doms":["a"]}`, 1, "doms"},
		{"3.x services", `{"count":1,"services":["a"]}`, 1, "services"},
		{"裸数组", `["a","b"]`, 2, "array"},
		{"未知键名扫描", `{"whatever":[{"name":"a"}]}`, 1, "whatever"},
	}
	for _, c := range cases {
		list, ok := parseServiceList([]byte(c.body))
		if !ok {
			t.Fatalf("%s: 解析失败", c.name)
		}
		if len(list.Items) != c.want || list.Source != c.source {
			t.Errorf("%s: got %d 条 source=%q, want %d/%q", c.name, len(list.Items), list.Source, c.want, c.source)
		}
		if c.want > 0 && list.Items[0].Name == "" {
			t.Errorf("%s: 服务名为空", c.name)
		}
	}
	for _, bad := range []string{`<html><body>login</body></html>`, `{"message":"ok"}`, ``} {
		if _, ok := parseServiceList([]byte(bad)); ok {
			t.Errorf("%q 不应被识别为服务列表", bad)
		}
	}
}

// TestServicesFallbackOnEmpty catalog 返回 200 且形状可解析、但一条都没有时，
// 也必须回落 service/list —— 这正是"服务列表一直是空的"的场景。
func TestServicesFallbackOnEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nacos/v1/auth/login":
			_, _ = w.Write([]byte(`{"accessToken":"tk","tokenTtl":18000}`))
		case "/nacos/v1/ns/catalog/services":
			_, _ = w.Write([]byte(`{"count":0,"serviceList":[]}`))
		case "/nacos/v1/ns/service/list":
			if r.URL.Query().Get("groupName") != "" {
				t.Errorf("兜底请求不应带空的 groupName: %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"count":1,"doms":["real-service"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cl, err := New(Config{Addr: srv.URL, Username: "nacos", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	list, err := cl.Services(context.Background(), "dev-ns", "", "", 1, 20)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if list.Source != "service-list" {
		t.Fatalf("期望回落到 service/list，实际 source=%q", list.Source)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "real-service" {
		t.Fatalf("兜底结果异常: %+v", list.Items)
	}
}

// TestV1Compat 锁定 Nacos 1.x 的兼容行为：
//  1. catalog 返回 {totalCount, pageItems} 形状也要能解析，否则服务列表会空
//  2. 配置接口必须带 tenant 参数，1.x 只认它，缺了就静默退回 public（表现为"切命名空间配置不变"）
func TestV1Compat(t *testing.T) {
	var cfgQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nacos/v1/auth/login":
			_, _ = w.Write([]byte(`{"accessToken":"tk","tokenTtl":18000}`))
		case "/nacos/v1/ns/catalog/services":
			// 1.x 形状：totalCount + pageItems
			_, _ = w.Write([]byte(`{"totalCount":1,"pageItems":[{"name":"s1","groupName":"DEFAULT_GROUP","clusterCount":1,"ipCount":1,"healthyCount":1}]}`))
		case "/nacos/v1/cs/configs":
			cfgQuery = r.URL.Query()
			if r.URL.Query().Get("tenant") != "dev-ns" {
				// 模拟 1.x 的真实行为：没有 tenant 就当 public 处理
				_, _ = w.Write([]byte(`{"totalCount":0,"pageItems":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"totalCount":1,"pageItems":[{"id":"1","dataId":"dev.yaml","group":"DEFAULT_GROUP","type":"yaml"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cl, err := New(Config{Addr: srv.URL, Username: "nacos", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	list, err := cl.Services(ctx, "dev-ns", "", "", 1, 20)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if list.Source != "catalog-1x" {
		t.Fatalf("未识别为 1.x 形状，source=%q", list.Source)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Name != "s1" {
		t.Fatalf("1.x 服务列表解析异常: %+v total=%d", list.Items, list.Total)
	}

	cfgs, total, err := cl.Configs(ctx, "dev-ns", "", "", 1, 20)
	if err != nil {
		t.Fatalf("Configs: %v", err)
	}
	if cfgQuery.Get("tenant") != "dev-ns" {
		t.Fatalf("配置接口缺少 tenant 参数: %v", cfgQuery)
	}
	if total != 1 || len(cfgs) != 1 || cfgs[0].DataID != "dev.yaml" {
		t.Fatalf("1.x 配置列表解析异常: %+v total=%d", cfgs, total)
	}
}

func TestWrongPassword(t *testing.T) {
	srv := fakeNacos(t, true)
	defer srv.Close()

	cl, err := New(Config{Addr: srv.URL, Username: "nacos", Password: "bad"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = cl.Namespaces(context.Background())
	if err == nil {
		t.Fatal("密码错误时期望报错")
	}
	if !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("错误信息不够明确: %v", err)
	}
}
