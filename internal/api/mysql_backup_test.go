package api

import (
	"strings"
	"testing"

	"warden/internal/store"
)

func TestApplyBackupReqKeepsOmittedFields(t *testing.T) {
	task := &store.MySQLBackup{
		Name: "旧名字", Schema: "shop", Tables: []string{"a", "b"},
		Keep: 5, Compress: false, Enabled: true, Freq: "weekly",
	}
	// 只改计划，不带 tables / name / keep
	if err := applyBackupReq(task, &mysqlBackupReq{Schema: "shop", Freq: "daily", Hour: 3}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(task.Tables) != 2 || task.Tables[0] != "a" {
		t.Fatalf("没传 tables 时应保留原选择，实际 %v", task.Tables)
	}
	if task.Name != "旧名字" {
		t.Fatalf("没传 name 时应保留原名，实际 %s", task.Name)
	}
	if task.Keep != 5 {
		t.Fatalf("没传 keep 时应保留原值，实际 %d", task.Keep)
	}
	if task.Compress {
		t.Fatal("没传 compress 时应保留原值 false")
	}
}

func TestApplyBackupReqClearsTables(t *testing.T) {
	task := &store.MySQLBackup{Name: "n", Schema: "shop", Tables: []string{"a"}}
	empty := []string{}
	if err := applyBackupReq(task, &mysqlBackupReq{Schema: "shop", Tables: &empty}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(task.Tables) != 0 {
		t.Fatalf("传空数组应改成整库，实际 %v", task.Tables)
	}
}

func TestApplyBackupReqNormalizesTables(t *testing.T) {
	task := &store.MySQLBackup{}
	tables := []string{" a ", "a", "", "b"}
	if err := applyBackupReq(task, &mysqlBackupReq{Schema: "shop", Tables: &tables}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(task.Tables) != 2 || task.Tables[0] != "a" || task.Tables[1] != "b" {
		t.Fatalf("应去空去重，实际 %v", task.Tables)
	}
	// 新建时没给名称 → 按库名兜底
	if task.Name != "shop 备份" {
		t.Fatalf("默认名称不对: %s", task.Name)
	}
	// 非法计划被收敛
	if task.Freq != "daily" || task.Hour != 0 || task.Weekday != 0 || task.MonthDay != 1 {
		t.Fatalf("计划未收敛: %+v", task)
	}
}

func TestApplyBackupReqRejectsBadInput(t *testing.T) {
	if err := applyBackupReq(&store.MySQLBackup{}, &mysqlBackupReq{Schema: " "}); err == nil ||
		!strings.Contains(err.Error(), "数据库") {
		t.Fatalf("空库名应报错，实际 %v", err)
	}
	bad := []string{strings.Repeat("t", 65)}
	if err := applyBackupReq(&store.MySQLBackup{}, &mysqlBackupReq{Schema: "shop", Tables: &bad}); err == nil {
		t.Fatal("超长表名应报错")
	}
}

func TestNextRunPtrFollowsEnabled(t *testing.T) {
	on := &store.MySQLBackup{Enabled: true, Freq: "daily", Hour: 2}
	if nextRunPtr(on) == nil {
		t.Fatal("启用状态应给出下次运行时间")
	}
	off := &store.MySQLBackup{Enabled: false, Freq: "daily", Hour: 2}
	if nextRunPtr(off) != nil {
		t.Fatal("停用状态不该排期，否则重新启用会补跑一堆过期任务")
	}
}
