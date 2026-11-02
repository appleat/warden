package mysqldb

import (
	"strings"
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }

func TestQuoteLiteral(t *testing.T) {
	cases := map[string]string{
		"abc":      `'abc'`,
		"it's":     `'it''s'`,
		`a\b`:      `'a\\b'`,
		"line":     `'line'`,
		"tab\tend": "'tab\tend'",
	}
	for in, want := range cases {
		if got := quoteLiteral(in); got != want {
			t.Fatalf("quoteLiteral(%q) = %q，期望 %q", in, got, want)
		}
	}

	// 反斜杠 + 单引号组合：先转义反斜杠，再处理引号，回放时语义不变
	if got := quoteLiteral(`a\'b`); got != `'a\\''b'` {
		t.Fatalf("转义顺序不对：%q", got)
	}
}

func TestSQLLiteral(t *testing.T) {
	if got := sqlLiteral(nil, "INT"); got != "NULL" {
		t.Fatalf("NULL 应输出 NULL，实际 %q", got)
	}
	// 数值类型的 []byte 不能套引号
	if got := sqlLiteral([]byte("42"), "BIGINT"); got != "42" {
		t.Fatalf("数值应原样输出，实际 %q", got)
	}
	// 字符串列里的数字依旧是字符串
	if got := sqlLiteral([]byte("42"), "VARCHAR"); got != "'42'" {
		t.Fatalf("字符串应加引号，实际 %q", got)
	}
	// 数值列里出现非数字时退回字符串，避免生成非法 SQL
	if got := sqlLiteral([]byte("1,2"), "INT"); got != "'1,2'" {
		t.Fatalf("非数字应加引号，实际 %q", got)
	}
	// 零点时间按日期输出，避免日期列带上 00:00:00
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	if got := sqlLiteral(day, "DATE"); got != "'2026-11-02'" {
		t.Fatalf("日期格式不对：%q", got)
	}
}

func TestCSVCell(t *testing.T) {
	if got := csvCell(nil, ""); got != "" {
		t.Fatalf("NULL 默认应为空串，实际 %q", got)
	}
	if got := csvCell(nil, `\N`); got != `\N` {
		t.Fatalf("NULL 标记未生效：%q", got)
	}
	if got := csvCell([]byte("abc"), ""); got != "abc" {
		t.Fatalf("字节串转换异常：%q", got)
	}
}

func TestSplitStatements(t *testing.T) {
	src := `-- 头部注释
INSERT INTO t VALUES ('a;b');
/* 块;注释 */ SELECT 2;
# 行注释
SELECT 'it''s';
INSERT INTO t VALUES ("x--y", 'p#q');
`
	got := SplitStatements(src)
	if len(got) != 4 {
		t.Fatalf("应切出 4 条语句，实际 %d：%#v", len(got), got)
	}
	if got[0] != "INSERT INTO t VALUES ('a;b')" {
		t.Fatalf("字符串里的分号不应切断语句：%q", got[0])
	}
	if got[1] != "SELECT 2" {
		t.Fatalf("块注释应被去掉：%q", got[1])
	}
	if got[2] != "SELECT 'it''s'" {
		t.Fatalf("重复引号转义处理错误：%q", got[2])
	}
	if !strings.Contains(got[3], `"x--y"`) || !strings.Contains(got[3], `'p#q'`) {
		t.Fatalf("字符串里的注释符不应被当成注释：%q", got[3])
	}
}

func TestSplitStatementsDropsCommentOnly(t *testing.T) {
	got := SplitStatements("-- 只有注释\n/* 还是注释 */\n;\n")
	if len(got) != 0 {
		t.Fatalf("纯注释不应产生语句：%#v", got)
	}
}

func TestBuildCreateTableBasic(t *testing.T) {
	ddl, err := BuildCreateTable(TableSpec{
		Schema:  "app",
		Table:   "users",
		Engine:  "InnoDB",
		Charset: "utf8mb4",
		Comment: "用户表",
		Columns: []ColumnSpec{
			{Name: "id", Type: "bigint", Unsigned: true, AutoIncrement: true, Comment: "主键"},
			{Name: "name", Type: "varchar", Length: "64", Nullable: false, Default: strPtr("''")},
			{Name: "bio", Type: "text", Nullable: true},
		},
		Indexes: []IndexSpec{{Name: "uk_name", Columns: []string{"name"}, Unique: true}},
	})
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	for _, want := range []string{
		"CREATE TABLE `app`.`users` (",
		"PRIMARY KEY (`id`)",
		"`id` bigint UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键'",
		"`name` varchar(64) NOT NULL DEFAULT ''",
		"`bio` text NULL",
		"UNIQUE KEY `uk_name` (`name`)",
		"ENGINE=InnoDB",
		"DEFAULT CHARSET=utf8mb4",
		"COMMENT='用户表'",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("缺少片段 %q：\n%s", want, ddl)
		}
	}
	// 主键必须排在其他定义之前
	if strings.Index(ddl, "PRIMARY KEY") > strings.Index(ddl, "`id` bigint") {
		t.Fatalf("主键应放在列定义之前：\n%s", ddl)
	}
}

func TestBuildCreateTableAutoIncrementGetsPrimary(t *testing.T) {
	ddl, err := BuildCreateTable(TableSpec{
		Table: "t",
		Columns: []ColumnSpec{
			{Name: "id", Type: "int", AutoIncrement: true},
			{Name: "v", Type: "varchar", Length: "10"},
		},
	})
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if !strings.Contains(ddl, "PRIMARY KEY (`id`)") {
		t.Fatalf("自增列未自动成为主键：\n%s", ddl)
	}
}

func TestBuildCreateTableRejectsBadInput(t *testing.T) {
	base := func(cols ...ColumnSpec) TableSpec {
		return TableSpec{Table: "t", Columns: cols}
	}
	cases := []struct {
		name string
		spec TableSpec
		want string
	}{
		{"空表名", TableSpec{Columns: []ColumnSpec{{Name: "a", Type: "int"}}}, "表名"},
		{"无字段", TableSpec{Table: "t"}, "至少"},
		{"类型注入", base(ColumnSpec{Name: "a", Type: "varchar); DROP TABLE x --"}), "不支持"},
		{"长度注入", base(ColumnSpec{Name: "a", Type: "varchar", Length: "10)); DROP TABLE x --"}), "长度"},
		{"枚举注入", base(ColumnSpec{Name: "a", Type: "enum", Length: "'a'); DROP TABLE x --"}), "取值"},
		{"字段名注入", base(ColumnSpec{Name: "a`; DROP TABLE x --", Type: "int"}), "字段名"},
		{"重复字段", base(ColumnSpec{Name: "a", Type: "int"}, ColumnSpec{Name: "a", Type: "int"}), "重复"},
		{"索引引用不存在字段", TableSpec{
			Table:   "t",
			Columns: []ColumnSpec{{Name: "a", Type: "int"}},
			Indexes: []IndexSpec{{Name: "idx", Columns: []string{"nope"}}},
		}, "不存在的字段"},
		{"NOT NULL 默认 NULL", base(ColumnSpec{Name: "a", Type: "int", Nullable: false, Default: strPtr("NULL")}), "不能为 NULL"},
	}
	for _, c := range cases {
		_, err := BuildCreateTable(c.spec)
		if err == nil {
			t.Fatalf("%s：应当报错", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：错误信息应包含 %q，实际 %q", c.name, c.want, err.Error())
		}
	}
}

func TestBuildCreateTableEnumAndDefault(t *testing.T) {
	ddl, err := BuildCreateTable(TableSpec{
		Table: "t",
		Columns: []ColumnSpec{
			{Name: "status", Type: "enum", Length: "'启用','停用'", Nullable: false, Default: strPtr("启用")},
			{Name: "created_at", Type: "datetime", Default: strPtr("CURRENT_TIMESTAMP")},
		},
	})
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if !strings.Contains(ddl, "`status` enum('启用','停用') NOT NULL DEFAULT '启用'") {
		t.Fatalf("枚举定义异常：\n%s", ddl)
	}
	if !strings.Contains(ddl, "`created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP") {
		t.Fatalf("时间戳默认值异常：\n%s", ddl)
	}
}

func TestSpecFromDefRoundTrip(t *testing.T) {
	def := columnDef{
		Name: "name", Type: "varchar(64)", Nullable: false,
		DefaultNull: false, Default: "", Comment: "名称",
		Charset: "utf8mb4", Collation: "utf8mb4_general_ci",
	}
	spec := specFromDef(def)
	if spec.Type != "varchar" || spec.Length != "64" {
		t.Fatalf("类型拆分错误：%+v", spec)
	}
	if spec.Default == nil || *spec.Default != "''" {
		t.Fatalf("空串默认值应保留为 ''：%+v", spec.Default)
	}
	body, err := columnDDL(spec)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if !strings.Contains(body, "DEFAULT ''") {
		t.Fatalf("空串默认值丢失：%s", body)
	}
	if !EqualColumnBody(spec, specFromDef(def)) {
		t.Fatalf("同一列往返比较应相等")
	}

	// 没有默认值的列不能被当成 DEFAULT ''
	noDefault := specFromDef(columnDef{Name: "v", Type: "int", Nullable: true, DefaultNull: true})
	if noDefault.Default != nil {
		t.Fatalf("无默认值列不应带 Default：%+v", noDefault.Default)
	}
}

func TestSpecFromDefUnsignedAndAutoIncrement(t *testing.T) {
	spec := specFromDef(columnDef{
		Name: "id", Type: "bigint unsigned", Nullable: false,
		DefaultNull: true, Extra: "auto_increment",
	})
	if !spec.Unsigned || !spec.AutoIncrement {
		t.Fatalf("无符号/自增未识别：%+v", spec)
	}
	if spec.Type != "bigint" || spec.Length != "" {
		t.Fatalf("类型拆分错误：%+v", spec)
	}

	onUpdate := specFromDef(columnDef{
		Name: "u", Type: "timestamp", Nullable: false,
		DefaultNull: false, Default: "CURRENT_TIMESTAMP", Extra: "on update CURRENT_TIMESTAMP",
	})
	if onUpdate.OnUpdate == "" {
		t.Fatalf("ON UPDATE 未识别：%+v", onUpdate)
	}
}

func TestNormalizeDesiredKeepsExistingAttributes(t *testing.T) {
	old := columnDef{
		Name: "name", Type: "varchar(64)", Nullable: false,
		DefaultNull: false, Default: "", Charset: "utf8mb4", Collation: "utf8mb4_general_ci",
	}
	// 用户只改了注释，没填字符集（默认值按前端回传的 '' 字面量给）
	got := normalizeDesired(ColumnSpec{
		Name: "name", Type: "varchar", Length: "64",
		Default: strPtr("''"), Comment: "改了注释",
	}, old)
	if got.Charset != "utf8mb4" || got.Collation != "utf8mb4_general_ci" {
		t.Fatalf("字符集应沿用原值：%+v", got)
	}
	// 除注释外的定义必须与原来完全一致，否则一次「只改注释」会顺带改掉别的东西
	a, b := got, specFromDef(old)
	a.Comment, b.Comment = "", ""
	if !EqualColumnBody(a, b) {
		t.Fatalf("除注释外的定义应完全一致：%+v vs %+v", a, b)
	}
}

func TestSameIndex(t *testing.T) {
	a := IndexSpec{Name: "idx_a", Columns: []string{"a", "b"}, Unique: false}
	if !sameIndex(a, IndexSpec{Name: "x", Columns: []string{"A", "B"}}) {
		t.Fatal("列名大小写不同应视为同一索引")
	}
	if sameIndex(a, IndexSpec{Columns: []string{"a"}, Unique: true}) {
		t.Fatal("唯一性与列数变化应视为不同")
	}
}

func TestImportValue(t *testing.T) {
	intCol := importColumn{Name: "n", Type: "int", Nullable: true}
	strCol := importColumn{Name: "s", Type: "varchar", Nullable: true}

	cases := []struct {
		raw  string
		col  importColumn
		want any
	}{
		{"", intCol, nil},
		{"", strCol, ""},
		{"NULL", strCol, nil},
		{`\N`, strCol, nil},
		{"  ", intCol, nil},
		{"123", intCol, "123"},
		{" abc ", strCol, " abc "},
	}
	for _, c := range cases {
		if got := importValue(c.raw, c.col, ""); got != c.want {
			t.Fatalf("importValue(%q, %s) = %#v，期望 %#v", c.raw, c.col.Type, got, c.want)
		}
	}
	// 自定义 NULL 标记
	if got := importValue("NIL", strCol, "NIL"); got != nil {
		t.Fatalf("自定义 NULL 标记未生效：%#v", got)
	}
}

func TestSplitColumnType(t *testing.T) {
	cases := []struct {
		in, base, arg string
		unsigned      bool
	}{
		{"varchar(64)", "varchar", "64", false},
		{"bigint unsigned", "bigint", "", true},
		{"int(10) unsigned", "int", "10", true},
		{"decimal(10,2)", "decimal", "10,2", false},
		{"text", "text", "", false},
		{"enum('a','b')", "enum", "'a','b'", false},
	}
	for _, c := range cases {
		b, a, u := splitColumnType(c.in)
		if b != c.base || a != c.arg || u != c.unsigned {
			t.Fatalf("splitColumnType(%q) = (%q,%q,%v)，期望 (%q,%q,%v)",
				c.in, b, a, u, c.base, c.arg, c.unsigned)
		}
	}
}
