package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"warden/internal/auth"
	"warden/internal/config"
	"warden/internal/middleware"
	"warden/internal/store"
)

// 配置内容格式校验接口只做本地解析，不依赖 Nacos 连接也不落库，
// 因此这里不需要假 Nacos 服务端（对比 redis_test.go 里的假 Redis）。

func newNacosValidateEnv(t *testing.T) (http.Handler, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := st.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	cfg := &config.Config{Mode: "server", DataDir: dir, FileRoot: dir, JWTSecret: "test-secret-key", TokenHours: 1}
	d := &Deps{Cfg: cfg, Store: st, Auth: auth.New(st, cfg.JWTSecret, cfg.TokenTTL()), Version: "test"}

	u, err := d.Auth.Bootstrap("admin", "admin12345")
	if err != nil {
		t.Fatalf("创建账号失败：%v", err)
	}
	token, err := d.Auth.Issue(u)
	if err != nil {
		t.Fatalf("签发令牌失败：%v", err)
	}

	r := gin.New()
	g := r.Group("/api")
	g.Use(middleware.Auth(d.Auth))
	{
		g.POST("/nacos/config/validate", d.nacosValidateConfig)
		g.PUT("/nacos/config", d.nacosPublishConfig)
	}
	return r, token
}

func doValidate(t *testing.T, r http.Handler, token string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/nacos/config/validate", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestNacosValidateConfigYAML(t *testing.T) {
	r, token := newNacosValidateEnv(t)

	// 合法 YAML
	code, out := doValidate(t, r, token, map[string]any{
		"type": "yaml", "data_id": "app.yaml",
		"content": "server:\n  port: 8080\n",
	})
	if code != http.StatusOK {
		t.Fatalf("合法 YAML 应返回 200，实际 %d：%v", code, out)
	}
	if data, _ := out["data"].(map[string]any); data["format"] != "yaml" {
		t.Fatalf("应回报 format=yaml，实际 %v", out["data"])
	}

	// 缩进错误：接口本身返回 200，由 valid=false + msg 表达校验结果
	code, out = doValidate(t, r, token, map[string]any{
		"type": "yaml", "data_id": "app.yaml",
		"content": "server:\n\tport: 8080\n",
	})
	if code != http.StatusOK {
		t.Fatalf("校验不通过也应返回 200（用 valid 表达结果），实际 %d：%v", code, out)
	}
	data, _ := out["data"].(map[string]any)
	if data["valid"] != false {
		t.Fatalf("应回报 valid=false，实际 %v", out["data"])
	}
	msg, _ := data["msg"].(string)
	if !strings.Contains(msg, "YAML") || !strings.Contains(msg, "第 2 行") {
		t.Fatalf("错误信息应包含格式与行号，实际 %q", msg)
	}

	// type 留空但 Data ID 是 .yml：仍按 YAML 校验
	code, out = doValidate(t, r, token, map[string]any{
		"type": "", "data_id": "application-prod.yml",
		"content": "a: 1\nb: c: d\n",
	})
	if code != http.StatusOK {
		t.Fatalf("后缀 .yml 也应校验，实际 %d：%v", code, out)
	}
	if d, _ := out["data"].(map[string]any); d["valid"] != false {
		t.Fatalf("后缀 .yml 的非法内容应 valid=false，实际 %v", out["data"])
	}

	// 非 YAML 格式不校验内容
	code, out = doValidate(t, r, token, map[string]any{
		"type": "properties", "data_id": "app.properties",
		"content": "随便写 {{ 也不管\n",
	})
	if code != http.StatusOK {
		t.Fatalf("非 YAML 不应校验，实际 %d：%v", code, out)
	}
	if d, _ := out["data"].(map[string]any); d["format"] != "text" || d["valid"] != true {
		t.Fatalf("应回报 format=text / valid=true，实际 %v", out["data"])
	}

	// 空内容放行（允许先建空配置）
	code, out = doValidate(t, r, token, map[string]any{"type": "yaml", "data_id": "empty.yaml", "content": ""})
	if code != http.StatusOK {
		t.Fatalf("空内容应放行，实际 %d", code)
	}
	if d, _ := out["data"].(map[string]any); d["valid"] != true {
		t.Fatalf("空内容应 valid=true，实际 %v", out["data"])
	}
}

func TestNacosValidateConfigRequiresAuth(t *testing.T) {
	r, _ := newNacosValidateEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/nacos/config/validate",
		strings.NewReader(`{"type":"yaml","content":"a: 1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应返回 401，实际 %d", w.Code)
	}
}

// 发布接口同样要拦住非法 YAML。
// 这里故意不配置 Nacos 连接：校验放在取连接之前，所以能稳定命中校验分支，
// 否则会先返回 412（未配置连接），测不到真正的拦截逻辑。
func TestNacosPublishConfigRejectsBadYAML(t *testing.T) {
	r, token := newNacosValidateEnv(t)

	put := func(body map[string]any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/api/nacos/config", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	code, out := put(map[string]any{
		"group": "DEFAULT_GROUP", "data_id": "app.yaml", "type": "yaml",
		"content": "server:\n\tport: 8080\n",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("非法 YAML 发布应被拦下（400），实际 %d：%v", code, out)
	}
	if msg, _ := out["msg"].(string); !strings.Contains(msg, "第 2 行") {
		t.Fatalf("错误信息应带行号，实际 %q", msg)
	}

	// 合法 YAML 会继续往下走，因为没配连接，返回 412 —— 说明没有被误拦
	code, out = put(map[string]any{
		"group": "DEFAULT_GROUP", "data_id": "app.yaml", "type": "yaml",
		"content": "server:\n  port: 8080\n",
	})
	if code != http.StatusPreconditionFailed {
		t.Fatalf("合法 YAML 应跳过校验、进到连接检查（412），实际 %d：%v", code, out)
	}
}
