package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"warden/internal/middleware"
	"warden/internal/store"
)

func TestNormFavPath(t *testing.T) {
	cases := map[string]string{
		"":              "/",
		"/":             "/",
		"/data/logs":    "/data/logs",
		"/data/logs/":   "/data/logs",
		"/data/logs///": "/data/logs",
		"data/logs":     "data/logs",
		`D:\dev\go`:     "D:/dev/go",
		`D:\dev\go\`:    "D:/dev/go",
		"D:":            "D:/",
		"  /var/www/  ": "/var/www",
	}
	for in, want := range cases {
		if got := normFavPath(in); got != want {
			t.Errorf("normFavPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFavName(t *testing.T) {
	cases := []struct{ path, given, want string }{
		{"/data/logs", "", "logs"},
		{"/data/logs/", "", "logs"},
		{"/", "", "/"},
		{"/var/www", "站点根目录", "站点根目录"},
		{"/var/www", "   ", "www"},
	}
	for _, c := range cases {
		if got := favName(c.path, c.given); got != c.want {
			t.Errorf("favName(%q,%q) = %q, want %q", c.path, c.given, got, c.want)
		}
	}
}

func favCtx(t *testing.T, method, target string, body any, uid uint) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if body != nil {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, target, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		c.Request = r
	} else {
		c.Request = httptest.NewRequest(method, target, nil)
	}
	c.Set(middleware.CtxUID, uid)
	return c, w
}

func TestFavScope(t *testing.T) {
	cases := []struct {
		query string
		want  string
		ok    bool
	}{
		{"", "host:1", true},
		{"host=2", "host:2", true},
		{"id=3", "ssh:3", true},
		{"id=3&host=2", "ssh:3", true}, // SSH 主机优先，和文件接口一致
		{"id=abc", "", false},
		{"host=0", "", false},
	}
	for _, c := range cases {
		ctx, _ := favCtx(t, "GET", "/api/files/favorites?"+c.query, nil, 1)
		got, ok := favScope(ctx)
		if ok != c.ok || got != c.want {
			t.Errorf("favScope(%q) = (%q,%v), want (%q,%v)", c.query, got, ok, c.want, c.ok)
		}
	}
}

func TestFileFavoriteLifecycle(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fav.db"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Store: st}

	add := func(uid uint, query string, body map[string]any) {
		t.Helper()
		c, w := favCtx(t, "POST", "/api/files/favorites?"+query, body, uid)
		d.addFileFavorite(c)
		if w.Code != 200 {
			t.Fatalf("add status %d: %s", w.Code, w.Body.String())
		}
	}
	list := func(uid uint, query string) []store.FileFavorite {
		t.Helper()
		c, w := favCtx(t, "GET", "/api/files/favorites?"+query, nil, uid)
		d.listFileFavorites(c)
		if w.Code != 200 {
			t.Fatalf("list status %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Data []store.FileFavorite `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Data
	}

	add(1, "host=1", map[string]any{"path": "/data/logs/"})
	add(1, "host=1", map[string]any{"path": "/data/logs"}) // 尾斜杠去重
	add(1, "host=1", map[string]any{"path": "/DATA/LOGS"}) // 大小写去重
	add(1, "host=1", map[string]any{"path": "/var/www"})
	add(1, "id=3", map[string]any{"path": "/data/logs"}) // 不同主机范围
	add(2, "host=1", map[string]any{"path": "/opt"})     // 另一个账号

	if got := len(list(1, "host=1")); got != 2 {
		t.Fatalf("host:1 收藏数 = %d, want 2", got)
	}
	if got := len(list(1, "id=3")); got != 1 {
		t.Fatalf("ssh:3 收藏数 = %d, want 1", got)
	}
	if got := len(list(2, "host=1")); got != 1 {
		t.Fatalf("账号 2 的收藏数 = %d, want 1", got)
	}

	// 别人的收藏删不掉：账号 2 试图删账号 1 的记录
	firstOfUID1 := list(1, "host=1")[0]
	c, w := favCtx(t, "DELETE", "/api/files/favorites/1", nil, 2)
	c.Params = gin.Params{{Key: "id", Value: itoa(firstOfUID1.ID)}}
	d.deleteFileFavorite(c)
	if w.Code != 404 {
		t.Fatalf("跨账号删除应 404，实际 %d", w.Code)
	}

	// 自己的可以删
	mine := list(1, "host=1")[0]
	c, w = favCtx(t, "DELETE", "/api/files/favorites/1", nil, 1)
	c.Params = gin.Params{{Key: "id", Value: itoa(mine.ID)}}
	d.deleteFileFavorite(c)
	if w.Code != 200 {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
	if got := len(list(1, "host=1")); got != 1 {
		t.Fatalf("删除后收藏数 = %d, want 1", got)
	}

	if sqlDB, err := st.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

func itoa(v uint) string { return strconv.FormatUint(uint64(v), 10) }
