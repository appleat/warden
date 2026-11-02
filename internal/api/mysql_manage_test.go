package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 这一组测试同样不连真实 MySQL：只验证「在连库之前就该拦下」的逻辑，
// 以及不需要数据库就能生成的 DDL 预览。

func TestMySQLExportRejectsWriteSQL(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(false))

	code, body := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/mysql/export?host=%d", env.hostID),
		`{"schema":"app","table":"t","sql":"UPDATE t SET a = 1","format":"csv"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("写语句导出应被拒绝，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "只能导出查询结果") {
		t.Fatalf("提示应说明只能导出查询：%v", body)
	}
}

func TestMySQLExportRequiresTarget(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(false))

	code, body := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/mysql/export?host=%d", env.hostID), `{"format":"csv"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("没指定目标应报错，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "请指定") {
		t.Fatalf("提示不明确：%v", body)
	}
}

func TestMySQLExportSQLNeedsTable(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(false))

	code, body := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/mysql/export?host=%d", env.hostID),
		`{"schema":"app","sql":"SELECT 1","format":"sql"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("SQL 格式缺表名应报错，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "必须指定库和表") {
		t.Fatalf("提示不明确：%v", body)
	}
}

func TestMySQLCreateTableDryRun(t *testing.T) {
	env := newMySQLTestEnv(t, nil)

	payload := `{"schema":"app","table":"orders",` +
		`"columns":[{"name":"id","type":"bigint","unsigned":true,"auto_increment":true,"nullable":false},` +
		`{"name":"amount","type":"decimal","length":"10,2","nullable":false,"default":"0.00"}],` +
		`"indexes":[{"name":"idx_amount","columns":["amount"],"unique":false,"kind":""}]}`
	code, body := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/mysql/table/create?dry_run=1&host=%d", env.hostID), payload)
	if code != http.StatusOK {
		t.Fatalf("预览建表失败 %d：%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	ddl, _ := data["ddl"].(string)
	if data["executed"] != false {
		t.Fatalf("dry_run 不应执行：%v", data)
	}
	for _, want := range []string{"CREATE TABLE `app`.`orders`", "PRIMARY KEY (`id`)", "idx_amount"} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("DDL 缺少 %q：\n%s", want, ddl)
		}
	}
}

func TestMySQLCreateTableBlocksInjection(t *testing.T) {
	env := newMySQLTestEnv(t, nil)

	cases := []string{
		`{"schema":"app","table":"t","columns":[{"name":"a","type":"varchar); DROP TABLE x --"}]}`,
		`{"schema":"app","table":"t","columns":[{"name":"a","type":"varchar","length":"10)); DROP TABLE x --"}]}`,
		`{"schema":"app","table":"t; DROP TABLE y --","columns":[{"name":"a","type":"int"}]}`,
	}
	for _, payload := range cases {
		code, body := env.do(t, http.MethodPost,
			fmt.Sprintf("/api/mysql/table/create?dry_run=1&host=%d", env.hostID), payload)
		if code != http.StatusBadRequest {
			t.Fatalf("注入内容应返回 400，实际 %d：%v", code, body)
		}
	}
}

func TestMySQLCreateTableReadOnly(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(true))

	payload := `{"schema":"app","table":"t","columns":[{"name":"a","type":"int"}]}`
	code, body := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/mysql/table/create?host=%d", env.hostID), payload)
	if code != http.StatusForbidden {
		t.Fatalf("只读模式下建表应返回 403，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "只读") {
		t.Fatalf("提示应说明只读模式：%v", body)
	}
}

func TestMySQLImportReadOnlyRejected(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(true))

	code, body := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/mysql/import?format=csv&schema=app&table=t&host=%d", env.hostID), "")
	if code != http.StatusForbidden {
		t.Fatalf("只读模式下导入应返回 403，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "只读") {
		t.Fatalf("提示应说明只读模式：%v", body)
	}
}

func TestMySQLImportNeedsMultipart(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(false))

	code, body := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/mysql/import?format=csv&schema=app&table=t&host=%d", env.hostID),
		`{"x":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非 multipart 应返回 400，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "multipart") {
		t.Fatalf("提示不明确：%v", body)
	}
}

func TestMySQLSlowDegradesWhenPerformanceSchemaUnavailable(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(true))

	code, body := env.do(t, http.MethodGet,
		fmt.Sprintf("/api/mysql/slow?host=%d", env.hostID), "")
	if code != http.StatusOK {
		t.Fatalf("读不到慢查询也应 200 并给出原因，实际 %d：%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["available"] != false {
		t.Fatalf("应标记为不可用：%v", body)
	}
	if msg, _ := data["error"].(string); msg == "" {
		t.Fatalf("应带上失败原因：%v", data)
	}
}

func TestMySQLSlowExplainGuards(t *testing.T) {
	env := newMySQLTestEnv(t, unreachableConf(false))

	// 语句模板里的 ? 没法直接 EXPLAIN
	code, body := env.do(t, http.MethodPost,
		fmt.Sprintf("/api/mysql/slow/explain?host=%d", env.hostID),
		`{"sql":"SELECT * FROM t WHERE id = ?"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("含 ? 的模板应返回 400，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "参数") {
		t.Fatalf("提示不明确：%v", body)
	}

	// 非 DML 语句不允许
	code, body = env.do(t, http.MethodPost,
		fmt.Sprintf("/api/mysql/slow/explain?host=%d", env.hostID),
		`{"sql":"DROP TABLE t"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("DDL 应返回 400，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "SELECT") {
		t.Fatalf("提示应说明允许的语句类型：%v", body)
	}
}
