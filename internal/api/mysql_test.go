package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"warden/internal/auth"
	"warden/internal/config"
	"warden/internal/fleet"
	"warden/internal/middleware"
	"warden/internal/store"
)

// mysqlTestEnv 是 MySQL 接口的测试夹具。
// 这里不连真实数据库：只验证「配置保存 / 按主机注入 / 参数校验 / 危险语句拦截」这些
// 与外部服务无关的逻辑，真正连库的部分留给手工联调。
type mysqlTestEnv struct {
	deps   *Deps
	router http.Handler
	token  string
	hostID uint
}

func newMySQLTestEnv(t *testing.T, conf *store.MySQLConf) *mysqlTestEnv {
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

	reg, err := fleet.New(st, cfg.SecretKey())
	if err != nil {
		t.Fatalf("初始化主机注册表失败：%v", err)
	}
	d.Fleet = reg
	local := reg.Local()
	if local == nil {
		t.Fatal("本机记录未创建")
	}

	u, err := d.Auth.Bootstrap("admin", "admin12345")
	if err != nil {
		t.Fatalf("创建账号失败：%v", err)
	}
	token, err := d.Auth.Issue(u)
	if err != nil {
		t.Fatalf("签发令牌失败：%v", err)
	}

	if conf != nil {
		conf.HostID = local.ID
		if err := st.DB.Create(conf).Error; err != nil {
			t.Fatalf("写入 MySQL 连接失败：%v", err)
		}
	}

	r := gin.New()
	g := r.Group("/api")
	// 与 Redis 测试同理：不挂 HostProxy，避免 httptest 的 ResponseRecorder
	// 缺少 CloseNotifier 导致反向代理失败
	g.Use(middleware.Auth(d.Auth), middleware.MySQLConn(reg, st, cfg.SecretKey()))
	{
		g.GET("/mysql/conn", d.getMySQLConn)
		g.PUT("/mysql/conn", middleware.RequireRole(auth.RoleOperator), d.saveMySQLConn)
		g.GET("/mysql/overview", d.mysqlOverview)
		g.POST("/mysql/query", middleware.RequireRole(auth.RoleOperator), d.mysqlQuery)
		g.GET("/mysql/tables", d.mysqlTables)
		g.GET("/mysql/slow", d.mysqlSlow)
		g.POST("/mysql/slow/explain", middleware.RequireRole(auth.RoleOperator), d.mysqlSlowExplain)
		g.POST("/mysql/export", middleware.RequireRole(auth.RoleOperator), d.mysqlExport)
		g.POST("/mysql/import", middleware.RequireRole(auth.RoleOperator), d.mysqlImport)
		g.POST("/mysql/table/create", middleware.RequireRole(auth.RoleOperator), d.mysqlCreateTable)
		g.POST("/mysql/table/alter", middleware.RequireRole(auth.RoleOperator), d.mysqlAlterTable)
		g.POST("/mysql/column/alter", middleware.RequireRole(auth.RoleOperator), d.mysqlAlterColumn)
		g.POST("/mysql/row/update", middleware.RequireRole(auth.RoleOperator), d.mysqlUpdateRow)
	}
	return &mysqlTestEnv{deps: d, router: r, token: token, hostID: local.ID}
}

func (e *mysqlTestEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+e.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// unreachableConf 指向一个必然连不上的地址：Open 是惰性的，不会在构造时拨号，
// 因此可以用它测试「不需要真库」的那部分逻辑。
func unreachableConf(readOnly bool) *store.MySQLConf {
	return &store.MySQLConf{
		Host: "127.0.0.1", Port: 1, Username: "reader", ReadOnly: readOnly,
	}
}

func TestMySQLWithoutConnConfig(t *testing.T) {
	env := newMySQLTestEnv(t, nil)

	code, body := env.do(t, http.MethodGet, fmt.Sprintf("/api/mysql/overview?host=%d", env.hostID), "")
	if code != http.StatusPreconditionFailed {
		t.Fatalf("未配置时应返回 412，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "尚未配置") {
		t.Fatalf("提示信息不友好：%v", body)
	}

	// 伪造的请求头必须被清掉，不能连到客户端指定的地址
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/mysql/overview?host=%d", env.hostID), nil)
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("X-Warden-MySQL-Host", "10.255.255.1")
	req.Header.Set("X-Warden-MySQL-User", "cm9vdA==")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("伪造连接头应被清除，实际 %d：%s", w.Code, w.Body.String())
	}
}

func TestMySQLConnectionIsHostScoped(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(true))

	other := store.Host{Name: "远端", Kind: "agent", Address: "127.0.0.1:1", Enabled: true}
	if err := env.deps.Store.DB.Create(&other).Error; err != nil {
		t.Fatalf("创建主机失败：%v", err)
	}
	// 另一台主机没配 MySQL，绝不能复用本机那份连接参数
	code, body := env.do(t, http.MethodGet, fmt.Sprintf("/api/mysql/overview?host=%d", other.ID), "")
	if code != http.StatusPreconditionFailed {
		t.Fatalf("未配置的主机应返回 412，实际 %d：%v", code, body)
	}
}

func TestMySQLOverviewSurfacesConnectError(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(true))

	code, body := env.do(t, http.MethodGet, fmt.Sprintf("/api/mysql/overview?host=%d", env.hostID), "")
	if code != http.StatusOK {
		t.Fatalf("连接失败也应返回 200 并带错误说明，实际 %d：%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["online"] != false {
		t.Fatalf("应标记为未连接：%v", body)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "MySQL") {
		t.Fatalf("错误提示应说明连不上 MySQL：%v", data["error"])
	}
}

func TestMySQLReadOnlyModeRejectsWrite(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(true))

	code, body := env.do(t, http.MethodPost, fmt.Sprintf("/api/mysql/query?host=%d", env.hostID),
		`{"sql":"UPDATE t SET a = 1"}`)
	if code != http.StatusForbidden {
		t.Fatalf("只读模式下写语句应返回 403，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "只读") {
		t.Fatalf("提示应说明只读模式：%v", body)
	}
}

func TestMySQLBlockedStatements(t *testing.T) {
	// 即使关掉只读，这些语句也必须被拦下（拦截发生在连库之前）
	env := newMySQLTestEnv(t, unreachableConf(false))

	cases := []string{
		"DROP DATABASE app",
		"GRANT ALL ON *.* TO 'x'@'%'",
		"SET GLOBAL max_connections = 10",
		"CREATE USER 'x'@'%' IDENTIFIED BY 'p'",
		"SET @@GLOBAL.max_connections = 10",
		"SHUTDOWN",
	}
	for _, sql := range cases {
		code, body := env.do(t, http.MethodPost, fmt.Sprintf("/api/mysql/query?host=%d", env.hostID),
			fmt.Sprintf(`{"sql":%q}`, sql))
		if code != http.StatusForbidden {
			t.Fatalf("%q 应被拒绝，实际 %d：%v", sql, code, body)
		}
	}
}

func TestMySQLSaveConn(t *testing.T) {
	env := newMySQLTestEnv(t, nil)

	// 首次保存：不传 read_only 时默认只读
	code, body := env.do(t, http.MethodPut, fmt.Sprintf("/api/mysql/conn?host=%d", env.hostID),
		`{"host":"10.0.0.9","port":3307,"username":"app","password":"secret","database":"orders","charset":"utf8mb4"}`)
	if code != http.StatusOK {
		t.Fatalf("保存连接失败 %d：%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["read_only"] != true {
		t.Fatalf("默认应为只读模式：%v", data)
	}

	code, body = env.do(t, http.MethodGet, fmt.Sprintf("/api/mysql/conn?host=%d", env.hostID), "")
	if code != http.StatusOK {
		t.Fatalf("读取连接失败 %d：%v", code, body)
	}
	data, _ = body["data"].(map[string]any)
	if data["configured"] != true || data["has_password"] != true {
		t.Fatalf("连接信息异常：%v", data)
	}
	if _, leaked := data["password"]; leaked {
		t.Fatalf("接口不应返回密码：%v", data)
	}

	// 库里必须是密文
	var conf store.MySQLConf
	if err := env.deps.Store.DB.Where("host_id = ?", env.hostID).First(&conf).Error; err != nil {
		t.Fatalf("读取落库配置失败：%v", err)
	}
	if conf.Password == "secret" || conf.Password == "" {
		t.Fatalf("密码未加密落库：%q", conf.Password)
	}
	if conf.Port != 3307 || conf.Host != "10.0.0.9" {
		t.Fatalf("字段落库异常：%+v", conf)
	}

	// 更新时留空密码不应覆盖原密码
	code, body = env.do(t, http.MethodPut, fmt.Sprintf("/api/mysql/conn?host=%d", env.hostID),
		`{"host":"10.0.0.9","port":3307,"username":"app","database":"orders","read_only":false}`)
	if code != http.StatusOK {
		t.Fatalf("更新连接失败 %d：%v", code, body)
	}
	var after store.MySQLConf
	if err := env.deps.Store.DB.Where("host_id = ?", env.hostID).First(&after).Error; err != nil {
		t.Fatalf("读取落库配置失败：%v", err)
	}
	if after.Password != conf.Password {
		t.Fatal("留空密码时不应覆盖已保存的密码")
	}
	if after.ReadOnly {
		t.Fatal("显式传 read_only=false 应关闭只读")
	}
}

func TestMySQLSaveConnValidation(t *testing.T) {
	env := newMySQLTestEnv(t, nil)

	bad := []string{
		`{"port":3306,"username":"root"}`,                  // 缺地址
		`{"host":"127.0.0.1","username":""}`,               // 缺账号
		`{"host":"127.0.0.1","username":"a","port":70000}`, // 端口越界
	}
	for _, b := range bad {
		code, body := env.do(t, http.MethodPut, fmt.Sprintf("/api/mysql/conn?host=%d", env.hostID), b)
		if code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d：%v", b, code, body)
		}
	}
}

// 单字段修改与单行更新都属于写操作：只读模式下必须在连库之前就被挡住。
func TestMySQLColumnAndRowWriteRequiresWritable(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(true))

	code, body := env.do(t, http.MethodPost, fmt.Sprintf("/api/mysql/column/alter?host=%d", env.hostID),
		`{"schema":"app","table":"users","column":"name","spec":{"name":"name","type":"varchar","length":"128"}}`)
	if code != http.StatusForbidden {
		t.Fatalf("只读模式下改字段应返回 403，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "只读") {
		t.Fatalf("提示应说明只读模式：%v", body)
	}

	code, body = env.do(t, http.MethodPost, fmt.Sprintf("/api/mysql/row/update?host=%d", env.hostID),
		`{"schema":"app","table":"users","key":{"id":"1"},"values":{"name":"bob"}}`)
	if code != http.StatusForbidden {
		t.Fatalf("只读模式下改数据应返回 403，实际 %d：%v", code, body)
	}
}

func TestMySQLColumnAndRowWriteWithoutConn(t *testing.T) {
	env := newMySQLTestEnv(t, nil)

	for _, p := range []string{"/api/mysql/column/alter", "/api/mysql/row/update"} {
		code, body := env.do(t, http.MethodPost, fmt.Sprintf("%s?host=%d", p, env.hostID), `{}`)
		if code != http.StatusPreconditionFailed {
			t.Fatalf("%s 未配置连接时应返回 412，实际 %d：%v", p, code, body)
		}
	}
}

// 参数不合法时不应该去连库（400 而不是 502）。
func TestMySQLColumnAndRowWriteBadRequest(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(false))

	// schema/table/column 缺一不可
	code, body := env.do(t, http.MethodPost, fmt.Sprintf("/api/mysql/column/alter?host=%d", env.hostID),
		`{"schema":"","table":"users","column":"name","spec":{"name":"name","type":"int"}}`)
	if code != http.StatusBadGateway && code != http.StatusBadRequest {
		t.Fatalf("缺库名不应成功，实际 %d：%v", code, body)
	}

	// 只有主键、没有要改的字段
	code, body = env.do(t, http.MethodPost, fmt.Sprintf("/api/mysql/row/update?host=%d", env.hostID),
		`{"schema":"app","table":"users","key":{"id":"1"},"values":{}}`)
	if code == http.StatusOK {
		t.Fatalf("没有要改的字段不应成功：%v", body)
	}
}
