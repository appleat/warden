package backup

import (
	"fmt"
	"strings"
	"time"

	"warden/internal/store"
)

// 计划类型。界面上对应的中文：每天 / 每周 / 每月。
const (
	FreqDaily   = "daily"
	FreqWeekly  = "weekly"
	FreqMonthly = "monthly"
)

// NextRun 计算下一次应该跑的时间（严格晚于 now）。
//
// 只按「服务器本地时区」算，不做时区配置：面板本来就是给这台服务器自己用的，
// 用户看到的时间与服务器 crontab 一致最不容易出错。
func NextRun(t *store.MySQLBackup, now time.Time) time.Time {
	hour := clamp(t.Hour, 0, 23)
	minute := clamp(t.Minute, 0, 59)
	loc := now.Location()

	switch strings.ToLower(strings.TrimSpace(t.Freq)) {
	case FreqWeekly:
		weekday := clamp(t.Weekday, 0, 6)
		cand := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		cand = cand.AddDate(0, 0, (weekday-int(now.Weekday())+7)%7)
		if !cand.After(now) {
			cand = cand.AddDate(0, 0, 7)
		}
		return cand

	case FreqMonthly:
		// 只允许 1-28：29-31 在小月不存在，要么漏跑要么得额外处理「顺延」，
		// 对备份这种「每月一次」的需求，1-28 完全够用且不会踩坑
		day := clamp(t.MonthDay, 1, 28)
		cand := time.Date(now.Year(), now.Month(), day, hour, minute, 0, 0, loc)
		if !cand.After(now) {
			cand = time.Date(now.Year(), now.Month()+1, day, hour, minute, 0, 0, loc)
		}
		return cand

	default: // daily
		cand := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		if !cand.After(now) {
			cand = cand.AddDate(0, 0, 1)
		}
		return cand
	}
}

// Normalize 把任务里的计划字段收敛到合法范围，顺手补默认值。
func Normalize(t *store.MySQLBackup) {
	switch strings.ToLower(strings.TrimSpace(t.Freq)) {
	case FreqWeekly, FreqMonthly, FreqDaily:
		t.Freq = strings.ToLower(strings.TrimSpace(t.Freq))
	default:
		t.Freq = FreqDaily
	}
	t.Hour = clamp(t.Hour, 0, 23)
	t.Minute = clamp(t.Minute, 0, 59)
	t.Weekday = clamp(t.Weekday, 0, 6)
	t.MonthDay = clamp(t.MonthDay, 1, 28)
	if t.Keep < 0 {
		t.Keep = 0
	}
	if t.Keep > 365 {
		t.Keep = 365
	}
}

// Describe 用中文描述计划，供列表与审计日志使用。
func Describe(t *store.MySQLBackup) string {
	at := fmt.Sprintf("%02d:%02d", clamp(t.Hour, 0, 23), clamp(t.Minute, 0, 59))
	switch strings.ToLower(strings.TrimSpace(t.Freq)) {
	case FreqWeekly:
		names := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
		return "每" + names[clamp(t.Weekday, 0, 6)] + " " + at
	case FreqMonthly:
		return fmt.Sprintf("每月 %d 日 %s", clamp(t.MonthDay, 1, 28), at)
	default:
		return "每天 " + at
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
