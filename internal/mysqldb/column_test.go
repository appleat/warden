package mysqldb

import (
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

// 字段修改：只改一个字段，生成 MODIFY / CHANGE，并做改名与冲突校验。
func TestBuildAlterColumn(t *testing.T) {
	// 可空且没写默认值时，information_schema 里 COLUMN_DEFAULT 是 NULL（DefaultNull=true）
	defs := []columnDef{
		{Name: "id", Type: "int(11)", Nullable: false, Extra: "auto_increment"},
		{Name: "name", Type: "varchar(64)", Nullable: true, DefaultNull: true, Charset: "utf8mb4", Collation: "utf8mb4_general_ci"},
		{Name: "code", Type: "varchar(16)", Nullable: true, DefaultNull: true, Charset: "utf8mb4", Collation: "utf8mb4_general_ci"},
		{Name: "note", Type: "text", Nullable: true, DefaultNull: true, Charset: "latin1", Collation: "latin1_swedish_ci"},
		{Name: "total", Type: "decimal(10,2)", Nullable: false, Extra: "STORED GENERATED"},
	}

	t.Run("只改注释", func(t *testing.T) {
		plan, err := buildAlterColumn("db", "t", defs[1], ColumnSpec{
			Name: "name", Type: "varchar", Length: "64", Nullable: true, Comment: "用户名",
		}, defs)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		want := "ALTER TABLE `db`.`t` MODIFY COLUMN `name` varchar(64) CHARACTER SET utf8mb4 " +
			"COLLATE utf8mb4_general_ci NULL COMMENT '用户名'"
		if plan.DDL != want {
			t.Fatalf("DDL 不符\n期望 %s\n实际 %s", want, plan.DDL)
		}
		if plan.Renamed {
			t.Fatal("不应标记为改名")
		}
	})

	t.Run("改名走 CHANGE COLUMN", func(t *testing.T) {
		plan, err := buildAlterColumn("db", "t", defs[1], ColumnSpec{
			Name: "nick", Type: "varchar", Length: "64", Nullable: true,
		}, defs)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.HasPrefix(plan.DDL, "ALTER TABLE `db`.`t` CHANGE COLUMN `name` `nick`") {
			t.Fatalf("改名应使用 CHANGE COLUMN，实际 %s", plan.DDL)
		}
		if !plan.Renamed {
			t.Fatal("应标记为改名")
		}
	})

	t.Run("改名撞上已有字段", func(t *testing.T) {
		_, err := buildAlterColumn("db", "t", defs[1], ColumnSpec{Name: "code", Type: "varchar", Length: "16"}, defs)
		if err == nil || !strings.Contains(err.Error(), "已存在") {
			t.Fatalf("应报字段已存在，实际 %v", err)
		}
	})

	t.Run("类型留空时沿用原类型", func(t *testing.T) {
		plan, err := buildAlterColumn("db", "t", defs[1], ColumnSpec{
			Name: "name", Nullable: true, Comment: "改个注释",
		}, defs)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.Contains(plan.DDL, "varchar(64)") {
			t.Fatalf("应沿用原类型，实际 %s", plan.DDL)
		}
	})

	t.Run("没有变化时返回空 DDL", func(t *testing.T) {
		plan, err := buildAlterColumn("db", "t", defs[1], ColumnSpec{
			Name: "name", Type: "varchar", Length: "64", Nullable: true,
		}, defs)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if plan.DDL != "" {
			t.Fatalf("应判定为无变化，实际 %s", plan.DDL)
		}
	})

	t.Run("字符集默认沿用原列的", func(t *testing.T) {
		plan, err := buildAlterColumn("db", "t", defs[3], ColumnSpec{
			Name: "note", Type: "text", Nullable: true,
		}, defs)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if plan.DDL != "" {
			t.Fatalf("只把字符集透传回来应视为无变化，实际 %s", plan.DDL)
		}
		plan, err = buildAlterColumn("db", "t", defs[3], ColumnSpec{
			Name: "note", Type: "text", Nullable: true, Comment: "备注",
		}, defs)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.Contains(plan.DDL, "CHARACTER SET latin1") || !strings.Contains(plan.DDL, "COLLATE latin1_swedish_ci") {
			t.Fatalf("应保留原字符集，实际 %s", plan.DDL)
		}
	})

	t.Run("生成列拒绝修改", func(t *testing.T) {
		_, err := buildAlterColumn("db", "t", defs[4], ColumnSpec{Name: "total", Type: "decimal", Length: "12,2"}, defs)
		if err == nil || !strings.Contains(err.Error(), "生成列") {
			t.Fatalf("应拒绝生成列，实际 %v", err)
		}
	})

	t.Run("zerofill 拒绝修改", func(t *testing.T) {
		z := columnDef{Name: "n", Type: "int(8) unsigned zerofill"}
		_, err := buildAlterColumn("db", "t", z, ColumnSpec{Name: "n", Type: "int", Length: "8"}, []columnDef{z})
		if err == nil || !strings.Contains(err.Error(), "ZEROFILL") {
			t.Fatalf("应拒绝 zerofill 列，实际 %v", err)
		}
	})

	t.Run("非法字段名", func(t *testing.T) {
		_, err := buildAlterColumn("db", "t", defs[1], ColumnSpec{Name: "a`b", Type: "int"}, defs)
		if err == nil {
			t.Fatal("应拒绝反引号字段名")
		}
	})

	t.Run("非法类型不会拼进 DDL", func(t *testing.T) {
		_, err := buildAlterColumn("db", "t", defs[1], ColumnSpec{
			Name: "name", Type: "varchar", Length: "10); DROP TABLE x --",
		}, defs)
		if err == nil {
			t.Fatal("类型参数注入应被拦下")
		}
	})
}

// 行更新：必须带齐主键、拒绝生成列、值区分 NULL 与空串、列顺序稳定。
func TestBuildUpdateRowSQL(t *testing.T) {
	defs := []columnDef{
		{Name: "id", Type: "int(11)"},
		{Name: "name", Type: "varchar(64)"},
		{Name: "memo", Type: "varchar(64)"},
		{Name: "total", Type: "decimal(10,2)", Extra: "VIRTUAL GENERATED"},
	}

	t.Run("单主键更新", func(t *testing.T) {
		q, args, err := buildUpdateRowSQL("db", "t", []string{"id"}, defs, UpdateRowRequest{
			Key:    map[string]*string{"id": ptr("7")},
			Values: map[string]*string{"name": ptr("bob")},
		})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		want := "UPDATE `db`.`t` SET `name` = ? WHERE `id` = ? LIMIT 1"
		if q != want {
			t.Fatalf("SQL 不符\n期望 %s\n实际 %s", want, q)
		}
		if len(args) != 2 || args[0] != "bob" || args[1] != "7" {
			t.Fatalf("参数不符：%#v", args)
		}
	})

	t.Run("NULL 与空串区分", func(t *testing.T) {
		q, args, err := buildUpdateRowSQL("db", "t", []string{"id"}, defs, UpdateRowRequest{
			Key:    map[string]*string{"id": ptr("1")},
			Values: map[string]*string{"memo": nil, "name": ptr("")},
		})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		// 列名排序后 memo 在前
		if !strings.Contains(q, "SET `memo` = ?, `name` = ?") {
			t.Fatalf("列顺序应按字典序稳定，实际 %s", q)
		}
		if args[0] != nil {
			t.Fatalf("memo 应为 NULL，实际 %#v", args[0])
		}
		if args[1] != "" {
			t.Fatalf("name 应为空串，实际 %#v", args[1])
		}
	})

	t.Run("复合主键", func(t *testing.T) {
		q, args, err := buildUpdateRowSQL("db", "t", []string{"id", "name"}, defs, UpdateRowRequest{
			Key:    map[string]*string{"id": ptr("1"), "name": ptr("a")},
			Values: map[string]*string{"memo": ptr("x")},
		})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.Contains(q, "WHERE `id` = ? AND `name` = ?") {
			t.Fatalf("复合主键条件不符：%s", q)
		}
		if len(args) != 3 {
			t.Fatalf("参数个数应为 3，实际 %d", len(args))
		}
	})

	t.Run("缺主键值", func(t *testing.T) {
		_, _, err := buildUpdateRowSQL("db", "t", []string{"id", "name"}, defs, UpdateRowRequest{
			Key:    map[string]*string{"id": ptr("1")},
			Values: map[string]*string{"memo": ptr("x")},
		})
		if err == nil || !strings.Contains(err.Error(), "缺少主键") {
			t.Fatalf("应报缺少主键，实际 %v", err)
		}
	})

	t.Run("主键为 NULL 拒绝", func(t *testing.T) {
		_, _, err := buildUpdateRowSQL("db", "t", []string{"id"}, defs, UpdateRowRequest{
			Key:    map[string]*string{"id": nil},
			Values: map[string]*string{"memo": ptr("x")},
		})
		if err == nil {
			t.Fatal("主键为 NULL 应拒绝")
		}
	})

	t.Run("不存在的字段", func(t *testing.T) {
		_, _, err := buildUpdateRowSQL("db", "t", []string{"id"}, defs, UpdateRowRequest{
			Key:    map[string]*string{"id": ptr("1")},
			Values: map[string]*string{"nope": ptr("x")},
		})
		if err == nil || !strings.Contains(err.Error(), "不存在") {
			t.Fatalf("应报字段不存在，实际 %v", err)
		}
	})

	t.Run("生成列拒绝写入", func(t *testing.T) {
		_, _, err := buildUpdateRowSQL("db", "t", []string{"id"}, defs, UpdateRowRequest{
			Key:    map[string]*string{"id": ptr("1")},
			Values: map[string]*string{"total": ptr("1")},
		})
		if err == nil || !strings.Contains(err.Error(), "生成列") {
			t.Fatalf("应拒绝生成列，实际 %v", err)
		}
	})

	t.Run("字段名大小写不敏感", func(t *testing.T) {
		q, _, err := buildUpdateRowSQL("db", "t", []string{"id"}, defs, UpdateRowRequest{
			Key:    map[string]*string{"ID": ptr("1")},
			Values: map[string]*string{"MEmo": ptr("x")},
		})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.Contains(q, "`memo` = ?") || !strings.Contains(q, "`id` = ?") {
			t.Fatalf("应还原成 information_schema 里的原始列名：%s", q)
		}
	})
}
