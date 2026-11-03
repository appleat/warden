package mysqldb

import (
	"context"
	"database/sql/driver"
	"io"
	"strings"
	"testing"

	"warden/internal/mysqltest"
)

// newFakeShop 造一台只有 shop 库的假 MySQL：
// orders / users 两张基础表 + 一个视图，覆盖「视图跳过」「转义」「数值列」几个点。
func newFakeShop() *mysqltest.Server {
	s := mysqltest.New("shop")
	// 按真实 MySQL 的脾气来：没带库限定名的 SELECT 一律 1046，
	// 这样「文件里用非限定名、查询却忘了加库名」这类 bug 能被测试挡住
	s.Strict = true
	s.AddTable("orders",
		"CREATE TABLE `orders` (\n  `id` int NOT NULL,\n  PRIMARY KEY (`id`)\n)",
		[]string{"id", "amount"},
		[]string{"INT", "DECIMAL"},
		[][]driver.Value{
			{int64(1), []byte("12.50")},
			{int64(2), []byte("0.00")},
		})
	s.AddTable("users",
		"CREATE TABLE `users` (\n  `id` int NOT NULL,\n  `name` varchar(64),\n  `note` text\n)",
		[]string{"id", "name", "note"},
		[]string{"INT", "VARCHAR", "TEXT"},
		[][]driver.Value{
			{int64(1), []byte("张三"), nil},
			{int64(2), []byte(`it's a \ test`), []byte("多行\n内容")},
		})
	s.AddView("v_users", "CREATE VIEW `v_users` AS select 1")
	return s
}

func TestDumpWholeDatabase(t *testing.T) {
	fake := newFakeShop()
	db := fake.DB()
	defer db.Close()

	var b strings.Builder
	stat, err := Dump(context.Background(), db, DumpOptions{Schema: "shop", WithDatabase: true}, &b)
	if err != nil {
		t.Fatalf("Dump 失败: %v", err)
	}
	out := b.String()

	if stat.Tables != 2 || stat.Rows != 4 {
		t.Fatalf("统计不对: tables=%d rows=%d", stat.Tables, stat.Rows)
	}
	if len(stat.Skipped) != 1 || stat.Skipped[0] != "v_users" {
		t.Fatalf("视图应被跳过，实际 %v", stat.Skipped)
	}

	// 建库语句带字符集，且切到目标库
	if !strings.Contains(out, "CREATE DATABASE IF NOT EXISTS `shop` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci;") {
		t.Fatalf("缺少建库语句:\n%s", out)
	}
	if !strings.Contains(out, "USE `shop`;") {
		t.Fatal("缺少 USE 语句")
	}
	// 有 USE 时用非限定名，方便恢复到别的库名
	if !strings.Contains(out, "DROP TABLE IF EXISTS `orders`;") {
		t.Fatal("缺少 DROP TABLE")
	}
	if strings.Contains(out, "`shop`.`orders`") {
		t.Fatal("有 USE 时不该再写库限定名")
	}
	// 表的顺序应与库内顺序一致
	if strings.Index(out, "`orders`") > strings.Index(out, "`users`") {
		t.Fatal("表顺序应按库内顺序")
	}
	if strings.Contains(out, "v_users") {
		t.Fatal("视图不该被导出")
	}

	// 转义：单引号翻倍、反斜杠翻倍、换行转义、NULL 原样
	if !strings.Contains(out, `'it''s a \\ test'`) {
		t.Fatalf("单引号/反斜杠转义不对:\n%s", out)
	}
	if !strings.Contains(out, `'多行\n内容'`) {
		t.Fatalf("换行转义不对:\n%s", out)
	}
	if !strings.Contains(out, "(1, '张三', NULL)") {
		t.Fatalf("NULL 与字符串处理不对:\n%s", out)
	}
	// DECIMAL 列不加引号
	if !strings.Contains(out, "(1, 12.50)") {
		t.Fatalf("数值列不该加引号:\n%s", out)
	}
	if !strings.Contains(out, "SET FOREIGN_KEY_CHECKS=1;") {
		t.Fatal("缺少收尾语句")
	}
	if stat.Bytes != int64(len(out)) {
		t.Fatalf("字节统计不对: %d vs %d", stat.Bytes, len(out))
	}
	// 一致性快照
	if execs := strings.ToUpper(strings.Join(fake.Execs(), ";")); !strings.Contains(execs, "CONSISTENT SNAPSHOT") {
		t.Fatalf("应尝试开启一致性快照，实际 %v", fake.Execs())
	}
}

func TestDumpSelectedTables(t *testing.T) {
	db := newFakeShop().DB()
	defer db.Close()

	var b strings.Builder
	stat, err := Dump(context.Background(), db, DumpOptions{
		Schema: "shop",
		Tables: []string{"users", "ghost"},
	}, &b)
	if err != nil {
		t.Fatalf("Dump 失败: %v", err)
	}
	out := b.String()

	if stat.Tables != 1 {
		t.Fatalf("只应导出 1 张表，实际 %d", stat.Tables)
	}
	if len(stat.Skipped) != 1 || stat.Skipped[0] != "ghost" {
		t.Fatalf("不存在的表应记录到 Skipped，实际 %v", stat.Skipped)
	}
	// 没有 WithDatabase 时用限定名
	if !strings.Contains(out, "DROP TABLE IF EXISTS `shop`.`users`;") {
		t.Fatalf("缺少限定名的 DROP TABLE:\n%s", out)
	}
	if strings.Contains(out, "CREATE DATABASE") || strings.Contains(out, "USE `shop`") {
		t.Fatal("指定表备份不该输出建库语句")
	}
	if strings.Contains(out, "orders") {
		t.Fatal("没选中的表不该被导出")
	}
}

func TestDumpRejectsEmptyTarget(t *testing.T) {
	db := newFakeShop().DB()
	defer db.Close()

	if _, err := Dump(context.Background(), db, DumpOptions{Schema: ""}, io.Discard); err == nil {
		t.Fatal("空库名应报错")
	}
	if _, err := Dump(context.Background(), db, DumpOptions{Schema: "shop", Tables: []string{"ghost"}}, io.Discard); err == nil {
		t.Fatal("选中的表全不存在时应报错")
	}
	if _, err := Dump(context.Background(), db, DumpOptions{Schema: "nothing"}, io.Discard); err == nil {
		t.Fatal("不存在的库应报错")
	}
}
