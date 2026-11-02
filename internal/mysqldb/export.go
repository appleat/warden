package mysqldb

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// MaxExportRows 是单次导出的行数上限。
// 全表导出没有上限会把 TB 级表拖成几十分钟的慢请求，这里给一个足够大但有限的天花板。
const MaxExportRows = 2000000

// ExportSource 指定数据来源：整表，或一条只读查询。
type ExportSource struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	SQL    string `json:"sql"`
}

// ExportOptions 控制导出内容。
type ExportOptions struct {
	Format           string `json:"format"`            // csv | sql
	BOM              bool   `json:"bom"`               // CSV 是否加 UTF-8 BOM（Excel 打开不乱码）
	NullText         string `json:"null_text"`         // CSV 里 NULL 的写法，空串表示留空
	IncludeStructure bool   `json:"include_structure"` // SQL：是否包含建表语句
	IncludeData      bool   `json:"include_data"`      // SQL：是否包含数据
	Limit            int    `json:"limit"`             // 0 表示按 MaxExportRows
}

// ExportStat 是导出结果统计。
type ExportStat struct {
	Format    string `json:"format"`
	Rows      int64  `json:"rows"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
	CostMS    int64  `json:"cost_ms"`
}

// countingWriter 统计写出字节数。
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Export 按 opt.Format 把数据写成 CSV 或 SQL 文本。
// 写失败时可能已经输出了一部分内容，调用方应直接把错误写到日志而不是改 HTTP 状态码。
func Export(ctx context.Context, db *sql.DB, src ExportSource, w io.Writer, opt ExportOptions) (*ExportStat, error) {
	format := strings.ToLower(strings.TrimSpace(opt.Format))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "sql" {
		return nil, fmt.Errorf("不支持的导出格式：%s（只支持 csv / sql）", opt.Format)
	}
	if format == "sql" {
		if strings.TrimSpace(src.Schema) == "" || strings.TrimSpace(src.Table) == "" {
			return nil, fmt.Errorf("导出为 SQL 时必须指定库和表")
		}
	}

	limit := opt.Limit
	if limit <= 0 || limit > MaxExportRows {
		limit = MaxExportRows
	}

	start := time.Now()
	cw := &countingWriter{w: w}
	stat := &ExportStat{Format: format}

	var err error
	if format == "csv" {
		err = exportCSV(ctx, db, src, cw, opt, limit, stat)
	} else {
		err = exportSQL(ctx, db, src, cw, opt, limit, stat)
	}
	stat.Bytes = cw.n
	stat.CostMS = time.Since(start).Milliseconds()
	return stat, err
}

// openSource 打开数据来源，并把行数上限下推到 SQL 的 LIMIT。
// 查询来源必须是只读语句，否则「导出」就变成了绕过权限的写入口。
func openSource(ctx context.Context, db *sql.DB, src ExportSource, limit int) (*sql.Rows, error) {
	var query string
	switch {
	case strings.TrimSpace(src.SQL) != "":
		stmt := strings.TrimSpace(src.SQL)
		for strings.HasSuffix(stmt, ";") {
			stmt = strings.TrimSpace(strings.TrimSuffix(stmt, ";"))
		}
		if !IsReadOnlyStatement(stmt) {
			return nil, fmt.Errorf("只能导出查询结果，写语句请使用 SQL 控制台")
		}
		if strings.Contains(stmt, ";") {
			return nil, fmt.Errorf("导出只支持单条语句")
		}
		// 不套子查询：SHOW / WITH 之类语句套进 FROM (...) 会直接语法错误，
		// 行数上限在读取时按 limit 截断即可。
		query = stmt
	case strings.TrimSpace(src.Table) != "":
		if strings.TrimSpace(src.Schema) == "" {
			return nil, fmt.Errorf("请指定数据库")
		}
		query = fmt.Sprintf("SELECT * FROM %s LIMIT %d", qualified(src.Schema, src.Table), limit)
	default:
		return nil, fmt.Errorf("请指定要导出的表或查询语句")
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, Friendly(err)
	}
	return rows, nil
}

// exportCSV 逐行写 CSV，列头取结果集的列名。
func exportCSV(ctx context.Context, db *sql.DB, src ExportSource, w io.Writer, opt ExportOptions, limit int, stat *ExportStat) error {
	rows, err := openSource(ctx, db, src, limit)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return Friendly(err)
	}
	if opt.BOM {
		if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
			return err
		}
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(cols); err != nil {
		return err
	}

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return Friendly(err)
		}
		record := make([]string, len(cols))
		for i, v := range vals {
			record[i] = csvCell(v, opt.NullText)
		}
		if err := cw.Write(record); err != nil {
			return err
		}
		stat.Rows++
		if int(stat.Rows) >= limit {
			stat.Truncated = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return Friendly(err)
	}
	cw.Flush()
	return cw.Error()
}

// csvCell 把驱动返回的值转成 CSV 单元格。CSV 天生分不清 NULL 与空串，
// 需要保留 NULL 语义时由调用方指定 NullText（例如 \N）。
func csvCell(v any, nullText string) string {
	switch t := v.(type) {
	case nil:
		return nullText
	case []byte:
		return string(t)
	case time.Time:
		return normalizeValue(t).(string)
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}

// exportSQL 生成可直接回放的 SQL 文本：可选建表语句 + 分批 INSERT。
func exportSQL(ctx context.Context, db *sql.DB, src ExportSource, w io.Writer, opt ExportOptions, limit int, stat *ExportStat) error {
	target := qualified(src.Schema, src.Table)

	includeStructure := opt.IncludeStructure
	includeData := opt.IncludeData
	// 两个都没勾时按「结构 + 数据」处理，避免导出一个空文件
	if !includeStructure && !includeData {
		includeStructure, includeData = true, true
	}

	// 先把可能失败的事情做完（取 DDL、打开结果集），再开始输出。
	// 一旦开始写就没法再改 HTTP 状态码，用户只能拿到一个残缺附件。
	var ddl string
	if includeStructure {
		ddl = showCreate(ctx, db, src.Schema, src.Table)
		if ddl == "" {
			return fmt.Errorf("没有取到建表语句，请检查账号是否有该表的权限")
		}
	}
	var rows *sql.Rows
	if includeData {
		var err error
		rows, err = openSource(ctx, db, src, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
	}

	header := fmt.Sprintf("-- Warden MySQL 导出\n-- 数据库: %s\n-- 表: %s\n-- 时间: %s\n"+
		"-- 恢复方式: mysql -u<user> -p <库名> < 本文件\n\n"+
		"SET NAMES utf8mb4;\n"+
		"SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE=REPLACE(@@SQL_MODE,'NO_BACKSLASH_ESCAPES','');\n"+
		"SET FOREIGN_KEY_CHECKS=0;\n\n",
		src.Schema, src.Table, time.Now().Format("2006-01-02 15:04:05"))
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}

	if includeStructure {
		if _, err := fmt.Fprintf(w, "DROP TABLE IF EXISTS %s;\n%s;\n\n", target, ddl); err != nil {
			return err
		}
	}

	if !includeData {
		_, err := io.WriteString(w, "SET FOREIGN_KEY_CHECKS=1;\nSET SQL_MODE=@OLD_SQL_MODE;\n")
		return err
	}

	cols, err := rows.Columns()
	if err != nil {
		return Friendly(err)
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return Friendly(err)
	}
	typeNames := make([]string, len(cols))
	for i := range types {
		if i < len(typeNames) {
			typeNames[i] = types[i].DatabaseTypeName()
		}
	}

	colList := make([]string, len(cols))
	for i, c := range cols {
		colList[i] = quoteIdent(c)
	}
	prefix := fmt.Sprintf("INSERT INTO %s (%s) VALUES\n", target, strings.Join(colList, ", "))

	const batchSize = 200
	batch := make([]string, 0, batchSize)
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}

	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := io.WriteString(w, prefix); err != nil {
			return err
		}
		if _, err := io.WriteString(w, strings.Join(batch, ",\n")); err != nil {
			return err
		}
		if _, err := io.WriteString(w, ";\n"); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return Friendly(err)
		}
		cells := make([]string, len(cols))
		for i, v := range vals {
			cells[i] = sqlLiteral(v, typeNames[i])
		}
		batch = append(batch, "("+strings.Join(cells, ", ")+")")
		stat.Rows++
		if len(batch) >= batchSize {
			if err := flushBatch(); err != nil {
				return err
			}
		}
		if int(stat.Rows) >= limit {
			stat.Truncated = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return Friendly(err)
	}
	if err := flushBatch(); err != nil {
		return err
	}
	if stat.Truncated {
		if _, err := fmt.Fprintf(w, "\n-- 已达到导出行数上限 %d，后续数据未包含\n", limit); err != nil {
			return err
		}
	}
	_, err = io.WriteString(w, "\nSET FOREIGN_KEY_CHECKS=1;\nSET SQL_MODE=@OLD_SQL_MODE;\n")
	return err
}

// isNumericType 判断驱动的数据库类型名是否为数值类型，
// 数值列不套引号，避免 STRICT 模式下把 '007' 之类写进整型列时被隐式截断。
func isNumericType(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT",
		"DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL", "YEAR", "BIT",
		"UNSIGNED TINYINT", "UNSIGNED SMALLINT", "UNSIGNED MEDIUMINT",
		"UNSIGNED INT", "UNSIGNED BIGINT":
		return true
	}
	return false
}

// plainNumber 判断字节串是否是一个合法的十进制数字字面量。
func plainNumber(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	i, dot := 0, 0
	if s[0] == '-' || s[0] == '+' {
		i = 1
	}
	if i >= len(s) {
		return false
	}
	for ; i < len(s); i++ {
		switch {
		case s[i] >= '0' && s[i] <= '9':
		case s[i] == '.':
			dot++
			if dot > 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// sqlLiteral 把值转成 SQL 字面量。空值统一写 NULL，字符串做反斜杠与单引号转义。
func sqlLiteral(v any, dbType string) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case time.Time:
		if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
			return quoteLiteral(t.Format("2006-01-02"))
		}
		return quoteLiteral(t.Format("2006-01-02 15:04:05"))
	case []byte:
		s := string(t)
		if isNumericType(dbType) && plainNumber(s) {
			return s
		}
		return quoteLiteral(s)
	case string:
		if isNumericType(dbType) && plainNumber(t) {
			return t
		}
		return quoteLiteral(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		if t {
			return "1"
		}
		return "0"
	default:
		return quoteLiteral(fmt.Sprint(v))
	}
}

// quoteLiteral 生成 MySQL 字符串字面量。
// 反斜杠转义为 \\、单引号写成 ”，两种写法组合在默认 sql_mode 下都成立；
// 文件头会先把 NO_BACKSLASH_ESCAPES 从 sql_mode 里摘掉，保证回放时转义语义一致。
func quoteLiteral(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString("''")
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case 0:
			b.WriteString(`\0`)
		case 0x1a:
			b.WriteString(`\Z`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
