package mysqldb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SlowQuery 是 performance_schema 里一条聚合后的「语句模板」。
type SlowQuery struct {
	SQL           string  `json:"sql"`
	Schema        string  `json:"schema"`
	Count         int64   `json:"count"`
	AvgMS         float64 `json:"avg_ms"`
	MaxMS         float64 `json:"max_ms"`
	TotalMS       float64 `json:"total_ms"`
	RowsExamined  int64   `json:"rows_examined"`
	RowsSent      int64   `json:"rows_sent"`
	NoIndexUsed   int64   `json:"no_index_used"`
	TmpDiskTables int64   `json:"tmp_disk_tables"`
	AvgExamined   float64 `json:"avg_examined"`
	FirstSeen     string  `json:"first_seen"`
	LastSeen      string  `json:"last_seen"`
}

// SlowSettings 是服务端与慢查询相关的开关，用来解释「为什么看不到慢 SQL」。
type SlowSettings struct {
	PerformanceSchema string `json:"performance_schema"`
	SlowLog           string `json:"slow_query_log"`
	LongQueryTime     string `json:"long_query_time"`
	SlowLogFile       string `json:"slow_query_log_file"`
	LogOutput         string `json:"log_output"`
	NoIndexLog        string `json:"log_queries_not_using_indexes"`
}

// SlowEntry 是 mysql.slow_log 表里的一条原始记录（log_output 含 TABLE 时才有）。
type SlowEntry struct {
	StartTime    string  `json:"start_time"`
	UserHost     string  `json:"user_host"`
	QueryTime    float64 `json:"query_time"`
	LockTime     float64 `json:"lock_time"`
	RowsSent     int64   `json:"rows_sent"`
	RowsExamined int64   `json:"rows_examined"`
	DB           string  `json:"db"`
	SQL          string  `json:"sql"`
}

// SlowReport 是慢查询页的完整数据。
type SlowReport struct {
	Available   bool         `json:"available"`
	Error       string       `json:"error,omitempty"`
	Hint        string       `json:"hint,omitempty"`
	Settings    SlowSettings `json:"settings"`
	Items       []SlowQuery  `json:"items"`
	Recent      []SlowEntry  `json:"recent"`
	RecentError string       `json:"recent_error,omitempty"`
}

// slowOrder 把前端排序键映射成 SQL 列，避免把用户输入直接拼进 ORDER BY。
var slowOrder = map[string]string{
	"avg":      "AVG_TIMER_WAIT",
	"max":      "MAX_TIMER_WAIT",
	"total":    "SUM_TIMER_WAIT",
	"count":    "COUNT_STAR",
	"examined": "SUM_ROWS_EXAMINED",
}

// picoToMS 把 performance_schema 的皮秒计时转成毫秒。
func picoToMS(v int64) float64 {
	return float64(v) / 1e9
}

// SlowQueries 读取慢 SQL 聚合与相关开关。
func SlowQueries(ctx context.Context, db *sql.DB, schema string, minAvgMS float64, order string, limit int) (*SlowReport, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	col, ok := slowOrder[strings.ToLower(strings.TrimSpace(order))]
	if !ok {
		col = "AVG_TIMER_WAIT"
	}

	rep := &SlowReport{Items: []SlowQuery{}, Recent: []SlowEntry{}}
	if vars, err := loadPairs(ctx, db, "SHOW GLOBAL VARIABLES"); err == nil {
		rep.Settings = SlowSettings{
			PerformanceSchema: vars["performance_schema"],
			SlowLog:           vars["slow_query_log"],
			LongQueryTime:     vars["long_query_time"],
			SlowLogFile:       vars["slow_query_log_file"],
			LogOutput:         vars["log_output"],
			NoIndexLog:        vars["log_queries_not_using_indexes"],
		}
	}

	q := `SELECT IFNULL(DIGEST_TEXT,''), IFNULL(SCHEMA_NAME,''), COUNT_STAR,
	             IFNULL(AVG_TIMER_WAIT,0), IFNULL(MAX_TIMER_WAIT,0), IFNULL(SUM_TIMER_WAIT,0),
	             IFNULL(SUM_ROWS_EXAMINED,0), IFNULL(SUM_ROWS_SENT,0),
	             IFNULL(SUM_NO_INDEX_USED,0), IFNULL(SUM_CREATED_TMP_DISK_TABLES,0),
	             IFNULL(DATE_FORMAT(FIRST_SEEN,'%Y-%m-%d %H:%i:%s'),''),
	             IFNULL(DATE_FORMAT(LAST_SEEN,'%Y-%m-%d %H:%i:%s'),'')
	      FROM performance_schema.events_statements_summary_by_digest
	      WHERE DIGEST_TEXT IS NOT NULL`
	args := []any{}
	if s := strings.TrimSpace(schema); s != "" {
		q += " AND SCHEMA_NAME = ?"
		args = append(args, s)
	}
	if minAvgMS > 0 {
		q += " AND AVG_TIMER_WAIT >= ?"
		args = append(args, int64(minAvgMS*1e9))
	}
	q += fmt.Sprintf(" ORDER BY %s DESC LIMIT %d", col, limit)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		rep.Available = false
		rep.Error = Friendly(err).Error()
		rep.Hint = slowHint(rep.Settings, err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var it SlowQuery
			var avg, max, total int64
			if err := rows.Scan(&it.SQL, &it.Schema, &it.Count, &avg, &max, &total,
				&it.RowsExamined, &it.RowsSent, &it.NoIndexUsed, &it.TmpDiskTables,
				&it.FirstSeen, &it.LastSeen); err != nil {
				return rep, Friendly(err)
			}
			it.AvgMS = picoToMS(avg)
			it.MaxMS = picoToMS(max)
			it.TotalMS = picoToMS(total)
			if it.Count > 0 {
				it.AvgExamined = float64(it.RowsExamined) / float64(it.Count)
			}
			rep.Items = append(rep.Items, it)
		}
		if err := rows.Err(); err != nil {
			return rep, Friendly(err)
		}
		rep.Available = true
	}

	// mysql.slow_log 只在 log_output 含 TABLE 时有内容，读不到不算错误
	rep.Recent, rep.RecentError = recentSlowLog(ctx, db, rep.Settings)
	return rep, nil
}

// slowHint 给出「看不到慢查询」的排查建议。
func slowHint(s SlowSettings, err error) string {
	msg := err.Error()
	if strings.Contains(msg, "1146") || strings.Contains(msg, "doesn't exist") {
		return "performance_schema 未开启或缺少该表：检查 my.cnf 里的 performance_schema=ON（需重启）"
	}
	if strings.Contains(msg, "1142") || strings.Contains(msg, "denied") {
		return "当前账号没有读取 performance_schema 的权限，请让 DBA 授予 SELECT 权限"
	}
	if strings.EqualFold(s.PerformanceSchema, "OFF") {
		return "performance_schema 处于 OFF 状态，需在服务端开启并重启"
	}
	return "可尝试在服务端开启 slow_query_log 后按 long_query_time 收集慢 SQL"
}

// recentSlowLog 读取 mysql.slow_log 的最近记录，取不到时返回原因。
func recentSlowLog(ctx context.Context, db *sql.DB, s SlowSettings) ([]SlowEntry, string) {
	if s.LogOutput != "" && !strings.Contains(strings.ToUpper(s.LogOutput), "TABLE") {
		return []SlowEntry{}, "log_output 为 " + s.LogOutput + "，慢日志写在文件里，面板无法读取"
	}
	rows, err := db.QueryContext(ctx, `
		SELECT IFNULL(DATE_FORMAT(start_time,'%Y-%m-%d %H:%i:%s'),''), IFNULL(user_host,''),
		       IFNULL(query_time,0), IFNULL(lock_time,0), IFNULL(rows_sent,0),
		       IFNULL(rows_examined,0), IFNULL(db,''), IFNULL(sql_text,'')
		FROM mysql.slow_log
		ORDER BY start_time DESC
		LIMIT 100`)
	if err != nil {
		return []SlowEntry{}, Friendly(err).Error()
	}
	defer rows.Close()

	out := make([]SlowEntry, 0, 32)
	for rows.Next() {
		var e SlowEntry
		if err := rows.Scan(&e.StartTime, &e.UserHost, &e.QueryTime, &e.LockTime,
			&e.RowsSent, &e.RowsExamined, &e.DB, &e.SQL); err != nil {
			return out, Friendly(err).Error()
		}
		if len(e.SQL) > 4000 {
			e.SQL = e.SQL[:4000] + " …"
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return out, Friendly(err).Error()
	}
	return out, ""
}
