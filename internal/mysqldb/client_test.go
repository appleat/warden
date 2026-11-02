package mysqldb

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestHeaderRoundTrip(t *testing.T) {
	h := http.Header{}
	// 中文库名与特殊字符都要能安全穿过请求头（头部只能是 ASCII，因此内部走 base64）
	in := Config{
		Host:     "10.0.0.8",
		Port:     3307,
		Username: "reader",
		Password: "p@ss:word/中文",
		Database: "订单库",
		Charset:  "utf8mb4",
		TLS:      true,
		Insecure: true,
		ReadOnly: true,
	}
	SetHeaders(h, in)

	for k, v := range h {
		for _, s := range v {
			if strings.ContainsFunc(s, func(r rune) bool { return r > 127 }) {
				t.Fatalf("请求头 %s 含非 ASCII 字符: %q", k, s)
			}
		}
	}

	out := ConfigFromHeaders(h)
	if out.Host != in.Host || out.Port != in.Port || out.Username != in.Username {
		t.Fatalf("基础字段不一致: %+v", out)
	}
	if out.Password != in.Password || out.Database != in.Database {
		t.Fatalf("密码或库名还原失败: %+v", out)
	}
	if !out.TLS || !out.Insecure || !out.ReadOnly || out.Charset != in.Charset {
		t.Fatalf("开关字段还原失败: %+v", out)
	}
}

func TestConfigAddr(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{Host: "127.0.0.1"}, "127.0.0.1:3306"},
		{Config{Host: "127.0.0.1", Port: 3307}, "127.0.0.1:3307"},
		{Config{Host: "::1", Port: 3306}, "[::1]:3306"},
		{Config{Host: " db.local ", Port: 99999}, "db.local:3306"},
	}
	for _, c := range cases {
		if got := c.cfg.Addr(); got != c.want {
			t.Errorf("Addr() = %q, want %q", got, c.want)
		}
	}
}

func TestEmpty(t *testing.T) {
	if !(Config{}).Empty() {
		t.Error("空配置应判定为未配置")
	}
	if (Config{Host: "127.0.0.1"}).Empty() {
		t.Error("填了地址不应判定为空")
	}
}

func TestReadOnlyStatement(t *testing.T) {
	readOnly := []string{
		"SELECT * FROM t",
		"  select 1",
		"SHOW TABLES",
		"/* hint */ SELECT 1",
		"-- 注释\nSELECT 1",
		"# 注释\nSELECT 1",
		"EXPLAIN SELECT 1",
		"WITH x AS (SELECT 1) SELECT * FROM x",
		"DESC t",
		"(select 1)",
	}
	for _, s := range readOnly {
		if !IsReadOnlyStatement(s) {
			t.Errorf("应判定为只读: %q", s)
		}
	}

	writes := []string{
		"INSERT INTO t VALUES (1)",
		"update t set a=1",
		"DELETE FROM t",
		"DROP TABLE t",
		"TRUNCATE TABLE t",
		"CREATE TABLE t2 (id int)",
		"ALTER TABLE t ADD c int",
	}
	for _, s := range writes {
		if IsReadOnlyStatement(s) {
			t.Errorf("不应判定为只读: %q", s)
		}
	}
}

func TestFirstKeywords(t *testing.T) {
	cases := []struct {
		sql  string
		want []string
	}{
		{"GRANT ALL ON *.* TO 'x'", []string{"grant", "all"}},
		{"/* c */ set global max_connections = 10", []string{"set", "global"}},
		{"Drop  Database `app`", []string{"drop", "database"}},
		{"select 1", []string{"select", "1"}},
	}
	for _, c := range cases {
		got := FirstKeywords(c.sql, 2)
		if len(got) != len(c.want) {
			t.Fatalf("%q => %v, want %v", c.sql, got, c.want)
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Fatalf("%q => %v, want %v", c.sql, got, c.want)
			}
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := quoteIdent("order"); got != "`order`" {
		t.Errorf("quoteIdent = %q", got)
	}
	// 反引号必须翻倍，否则可以提前闭合标识符
	if got := quoteIdent("a`b"); got != "`a``b`" {
		t.Errorf("quoteIdent 未转义反引号: %q", got)
	}
	if got := qualified("db", "t"); got != "`db`.`t`" {
		t.Errorf("qualified = %q", got)
	}
}

func TestOpenBuildsDSN(t *testing.T) {
	db, err := Open(Config{
		Host: "127.0.0.1", Port: 3306, Username: "u", Password: "p",
		Database: "订单 库", TLS: true, Insecure: true,
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer db.Close()
	if db.Stats().MaxOpenConnections != 4 {
		t.Errorf("连接池上限应为 4，实际 %d", db.Stats().MaxOpenConnections)
	}
	// Open 不应真正建立连接，因此这里不 Ping（单元测试环境没有 MySQL）
	_ = context.Background()
}

func TestFriendly(t *testing.T) {
	if Friendly(nil) != nil {
		t.Error("nil 应返回 nil")
	}
	err := Friendly(&mysql.MySQLError{Number: 1045, Message: "Access denied"})
	if !strings.Contains(err.Error(), "账号或密码错误") {
		t.Errorf("1045 未翻译: %v", err)
	}
	err = Friendly(errors.New("dial tcp 1.2.3.4:3306: connect: connection refused"))
	if !strings.Contains(err.Error(), "无法连接到 MySQL") {
		t.Errorf("连接被拒绝未翻译: %v", err)
	}
}
