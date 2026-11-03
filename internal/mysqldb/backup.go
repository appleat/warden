package mysqldb

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"
)

// DumpOptions 描述一次备份的内容。
type DumpOptions struct {
	Schema string   `json:"schema"`
	Tables []string `json:"tables"` // 空表示整库（只含基础表，视图自动跳过）
	// WithDatabase 为 true 时额外输出 CREATE DATABASE / USE，
	// 生成的脚本可以直接 mysql < file.sql 恢复到一个干净的实例。
	WithDatabase bool `json:"with_database"`
}

// DumpStat 是备份结果统计。
type DumpStat struct {
	Schema  string   `json:"schema"`
	Tables  int      `json:"tables"`
	Rows    int64    `json:"rows"`
	Bytes   int64    `json:"bytes"`
	Skipped []string `json:"skipped"`
	CostMS  int64    `json:"cost_ms"`
}

// querier 让同一套读取逻辑既能跑在 *sql.DB 上，也能跑在 *sql.Conn 上
// （备份全程用一条连接，才能保证一致性快照覆盖所有表）。
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// dumpBatchRows 是单条 INSERT 里的行数上限。
const dumpBatchRows = 200

// dumpBatchBytes 是单条 INSERT 的字节上限。
// 行数够小但单行特别大（BLOB）时，仅靠行数会把语句顶到 max_allowed_packet 之上。
const dumpBatchBytes = 1 << 20

// Dump 把整库或指定表导出成可回放的 SQL 文本。
//
// 与 mysqldump --single-transaction 同思路：所有读取走同一条连接上的一致性快照事务，
// InnoDB 表之间处于同一时点。开不了快照（老版本 / 特殊引擎）就退化成普通读取，
// 备份依然完整，只是跨表不严格同一时点。
func Dump(ctx context.Context, db *sql.DB, opt DumpOptions, w io.Writer) (*DumpStat, error) {
	schema := strings.TrimSpace(opt.Schema)
	if schema == "" {
		return nil, fmt.Errorf("请指定要备份的数据库")
	}

	start := time.Now()
	stat := &DumpStat{Schema: schema}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, Friendly(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err == nil {
		if _, err := conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT"); err == nil {
			// ctx 可能已被取消，收尾用独立上下文，避免把连接留在事务里
			defer func() { _, _ = conn.ExecContext(context.Background(), "COMMIT") }()
		}
	}

	base, views, err := listBaseTables(ctx, conn, schema)
	if err != nil {
		return nil, err
	}
	if len(base) == 0 {
		return nil, fmt.Errorf("数据库 %s 里没有可备份的表", schema)
	}

	targets := make([]string, 0, len(base))
	if len(opt.Tables) == 0 {
		targets = append(targets, base...)
	} else {
		seen := map[string]bool{}
		for _, t := range opt.Tables {
			t = strings.TrimSpace(t)
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			if containsString(base, t) {
				targets = append(targets, t)
				continue
			}
			stat.Skipped = append(stat.Skipped, t)
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("选中的表在数据库 %s 里都不存在（可能已被删除或改名），请重新选择", schema)
		}
	}
	// 用户勾选顺序不代表建表顺序；按库里的顺序导出，外键与触发器更容易回放
	ordered := make([]string, 0, len(targets))
	for _, t := range base {
		if containsString(targets, t) {
			ordered = append(ordered, t)
		}
	}
	targets = ordered
	if len(views) > 0 && len(opt.Tables) == 0 {
		stat.Skipped = append(stat.Skipped, views...)
	}

	cw := &countingWriter{w: w}
	if err := writeDumpHeader(ctx, conn, schema, targets, opt, cw); err != nil {
		return nil, err
	}

	for _, table := range targets {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("备份已中断：%w", err)
		}
		if err := dumpTable(ctx, conn, schema, table, opt.WithDatabase, cw, stat); err != nil {
			return nil, err
		}
		stat.Tables++
	}

	if _, err := io.WriteString(cw, dumpFooter); err != nil {
		return nil, err
	}
	stat.Bytes = cw.n
	stat.CostMS = time.Since(start).Milliseconds()
	return stat, nil
}

const dumpFooter = "\nSET UNIQUE_CHECKS=1;\nSET FOREIGN_KEY_CHECKS=1;\nSET SQL_MODE=@OLD_SQL_MODE;\n"

// listBaseTables 返回基础表（按名字排序）与视图列表。
// 视图的 SHOW CREATE 得到的是 CREATE VIEW，套上 DROP TABLE 会直接报错，因此单独拎出来跳过。
func listBaseTables(ctx context.Context, q querier, schema string) (base, views []string, err error) {
	rows, err := q.QueryContext(ctx,
		`SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES
		 WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, schema)
	if err != nil {
		return nil, nil, Friendly(err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, nil, Friendly(err)
		}
		if strings.EqualFold(typ, "VIEW") {
			views = append(views, name)
			continue
		}
		base = append(base, name)
	}
	return base, views, rows.Err()
}

// writeDumpHeader 输出文件头：来源说明 + 会话设置 + （可选）建库语句。
func writeDumpHeader(ctx context.Context, q querier, schema string, tables []string, opt DumpOptions, w io.Writer) error {
	scope := fmt.Sprintf("整库（%d 张表）", len(tables))
	if len(opt.Tables) > 0 {
		scope = fmt.Sprintf("指定表（%d 张）: %s", len(tables), strings.Join(tables, ", "))
	}

	var b strings.Builder
	b.WriteString("-- Warden MySQL 备份\n")
	fmt.Fprintf(&b, "-- 数据库: %s\n", schema)
	fmt.Fprintf(&b, "-- 范围: %s\n", scope)
	fmt.Fprintf(&b, "-- 时间: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "-- 恢复: mysql -u<user> -p %s < 本文件\n", schema)
	b.WriteString("-- 注意: 备份包含 DROP TABLE，恢复到已有库会覆盖同名表\n\n")
	b.WriteString("SET NAMES utf8mb4;\n")
	b.WriteString("SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE=REPLACE(@@SQL_MODE,'NO_BACKSLASH_ESCAPES','');\n")
	b.WriteString("SET FOREIGN_KEY_CHECKS=0;\n")
	b.WriteString("SET UNIQUE_CHECKS=0;\n\n")

	if opt.WithDatabase {
		charset, collation := schemaCharset(ctx, q, schema)
		if charset != "" {
			fmt.Fprintf(&b, "CREATE DATABASE IF NOT EXISTS %s DEFAULT CHARACTER SET %s", quoteIdent(schema), charset)
			if collation != "" {
				fmt.Fprintf(&b, " COLLATE %s", collation)
			}
			b.WriteString(";\n")
		}
		fmt.Fprintf(&b, "USE %s;\n\n", quoteIdent(schema))
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// schemaCharset 取库的默认字符集与排序规则，取不到就留空（建库语句里省略）。
func schemaCharset(ctx context.Context, q querier, schema string) (charset, collation string) {
	row := q.QueryRowContext(ctx,
		`SELECT IFNULL(DEFAULT_CHARACTER_SET_NAME,''), IFNULL(DEFAULT_COLLATION_NAME,'')
		 FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`, schema)
	_ = row.Scan(&charset, &collation)
	return charset, collation
}

// dumpTable 输出一张表的建表语句与全部数据。
func dumpTable(ctx context.Context, q querier, schema, table string, unqualified bool, w io.Writer, stat *DumpStat) error {
	// 查询一律用「库.表」限定名：备份连接上没有默认库（默认库可能为空、也可能
	// 是另一个库），少了库名真实 MySQL 会直接报 1046 No database selected。
	// 是否省略库名只影响写进文件的文本（文件头有 USE 时用非限定名，
	// 恢复时改成别的库名也不会被原库名钉死）。
	queryTarget := qualified(schema, table)
	textTarget := queryTarget
	if unqualified {
		textTarget = quoteIdent(table)
	}

	ddl, err := showCreateOn(ctx, q, schema, table)
	if err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "-- ----------------------------\n-- 表: %s\n-- ----------------------------\n", table)
	fmt.Fprintf(&b, "DROP TABLE IF EXISTS %s;\n%s;\n\n", textTarget, ddl)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}

	rows, err := q.QueryContext(ctx, "SELECT * FROM "+queryTarget)
	if err != nil {
		return fmt.Errorf("读取表 %s 失败：%w", table, Friendly(err))
	}
	defer rows.Close()

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
	prefix := fmt.Sprintf("INSERT INTO %s (%s) VALUES\n", textTarget, strings.Join(colList, ", "))

	batch := make([]string, 0, dumpBatchRows)
	batchBytes := 0
	flush := func() error {
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
		batchBytes = 0
		return nil
	}

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("读取表 %s 失败：%w", table, Friendly(err))
		}
		cells := make([]string, len(cols))
		size := 0
		for i, v := range vals {
			cells[i] = sqlLiteral(v, typeNames[i])
			size += len(cells[i]) + 2
		}
		batch = append(batch, "("+strings.Join(cells, ", ")+")")
		batchBytes += size
		stat.Rows++
		if len(batch) >= dumpBatchRows || batchBytes >= dumpBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("读取表 %s 失败：%w", table, Friendly(err))
	}
	if err := flush(); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n")
	return err
}

// showCreateOn 取建表语句；拿不到就直接报错——
// 备份里少了建表语句，恢复时等于丢了一张表，不能静默跳过。
func showCreateOn(ctx context.Context, q querier, schema, table string) (string, error) {
	rows, err := q.QueryContext(ctx, "SHOW CREATE TABLE "+qualified(schema, table))
	if err != nil {
		return "", fmt.Errorf("读取表 %s 的建表语句失败：%w", table, Friendly(err))
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil || len(cols) < 2 {
		return "", fmt.Errorf("读取表 %s 的建表语句失败", table)
	}
	if !rows.Next() {
		return "", fmt.Errorf("表 %s 的建表语句为空", table)
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", fmt.Errorf("读取表 %s 的建表语句失败：%w", table, Friendly(err))
	}
	if strings.TrimSpace(vals[1].String) == "" {
		return "", fmt.Errorf("表 %s 的建表语句为空", table)
	}
	return vals[1].String, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
