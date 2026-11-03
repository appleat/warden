package backup

import (
	"testing"
	"time"

	"warden/internal/store"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		t.Fatalf("解析时间失败: %v", err)
	}
	return v
}

func TestNextRunDaily(t *testing.T) {
	task := &store.MySQLBackup{Freq: FreqDaily, Hour: 2, Minute: 30}

	// 今天还没到点 → 今天
	if got := NextRun(task, at(t, "2026-11-03 01:00")); !got.Equal(at(t, "2026-11-03 02:30")) {
		t.Fatalf("应排到今天 02:30，实际 %s", got)
	}
	// 已过点 → 明天
	if got := NextRun(task, at(t, "2026-11-03 10:00")); !got.Equal(at(t, "2026-11-04 02:30")) {
		t.Fatalf("应排到明天 02:30，实际 %s", got)
	}
	// 正好到点：必须严格晚于 now，否则会同一分钟内重复触发
	if got := NextRun(task, at(t, "2026-11-03 02:30")); !got.Equal(at(t, "2026-11-04 02:30")) {
		t.Fatalf("到点时应排到明天，实际 %s", got)
	}
	// 月末跨月
	if got := NextRun(task, at(t, "2026-11-30 23:00")); !got.Equal(at(t, "2026-12-01 02:30")) {
		t.Fatalf("跨月不对，实际 %s", got)
	}
}

func TestNextRunWeekly(t *testing.T) {
	// 2026-11-03 是周二；weekday=1 表示周一
	task := &store.MySQLBackup{Freq: FreqWeekly, Weekday: 1, Hour: 3}

	if got := NextRun(task, at(t, "2026-11-03 10:00")); !got.Equal(at(t, "2026-11-09 03:00")) {
		t.Fatalf("应排到下周一 03:00，实际 %s", got)
	}
	// 当天就是周一，还没到点 → 今天
	if got := NextRun(task, at(t, "2026-11-09 01:00")); !got.Equal(at(t, "2026-11-09 03:00")) {
		t.Fatalf("当天未到点应排今天，实际 %s", got)
	}
	// 当天就是周一，已过点 → 下周一
	if got := NextRun(task, at(t, "2026-11-09 05:00")); !got.Equal(at(t, "2026-11-16 03:00")) {
		t.Fatalf("当天已过点应排下周，实际 %s", got)
	}
	// 周日（0）也要能算对
	sun := &store.MySQLBackup{Freq: FreqWeekly, Weekday: 0, Hour: 4}
	if got := NextRun(sun, at(t, "2026-11-03 10:00")); !got.Equal(at(t, "2026-11-08 04:00")) {
		t.Fatalf("周日任务应排到 11-08 04:00，实际 %s", got)
	}
}

func TestNextRunMonthly(t *testing.T) {
	task := &store.MySQLBackup{Freq: FreqMonthly, MonthDay: 15, Hour: 1}

	if got := NextRun(task, at(t, "2026-11-03 10:00")); !got.Equal(at(t, "2026-11-15 01:00")) {
		t.Fatalf("应排到 11-15 01:00，实际 %s", got)
	}
	if got := NextRun(task, at(t, "2026-11-20 10:00")); !got.Equal(at(t, "2026-12-15 01:00")) {
		t.Fatalf("应排到 12-15 01:00，实际 %s", got)
	}
	// 跨年
	if got := NextRun(task, at(t, "2026-12-20 10:00")); !got.Equal(at(t, "2027-01-15 01:00")) {
		t.Fatalf("跨年不对，实际 %s", got)
	}
	// 31 号被收敛到 28，避免小月空跑
	task.MonthDay = 31
	if got := NextRun(task, at(t, "2026-02-01 00:00")); !got.Equal(at(t, "2026-02-28 01:00")) {
		t.Fatalf("31 号应收敛到 28，实际 %s", got)
	}
}

func TestNormalizeAndDescribe(t *testing.T) {
	task := &store.MySQLBackup{Freq: "WEEKLY", Hour: 99, Minute: -5, Weekday: 9, MonthDay: 0, Keep: -1}
	Normalize(task)
	if task.Freq != FreqWeekly || task.Hour != 23 || task.Minute != 0 ||
		task.Weekday != 6 || task.MonthDay != 1 || task.Keep != 0 {
		t.Fatalf("收敛结果不对: %+v", task)
	}

	if got := Describe(&store.MySQLBackup{Freq: FreqDaily, Hour: 2, Minute: 5}); got != "每天 02:05" {
		t.Fatalf("daily 描述不对: %s", got)
	}
	if got := Describe(&store.MySQLBackup{Freq: FreqWeekly, Weekday: 3, Hour: 1}); got != "每周三 01:00" {
		t.Fatalf("weekly 描述不对: %s", got)
	}
	if got := Describe(&store.MySQLBackup{Freq: FreqMonthly, MonthDay: 7, Hour: 23, Minute: 30}); got != "每月 7 日 23:30" {
		t.Fatalf("monthly 描述不对: %s", got)
	}
}
