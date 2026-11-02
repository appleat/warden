package mysqldb

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// MaxImportFileBytes 是单次导入的文件大小上限。
// 面板不是批量迁移工具，几十 MB 的 dump 已经够日常补数据用了。
const MaxImportFileBytes = 64 << 20

// importBatchSize 是 CSV 导入的批量插入行数，再大容易顶到 max_allowed_packet。
const importBatchSize = 200

// ImportOptions 控制导入行为。
type ImportOptions struct {
	Format    string `json:"format"`     // csv | sql
	Schema    string `json:"schema"`     // csv 必填；sql 作为 USE 之前的默认库
	Table     string `json:"table"`      // csv 必填
	HasHeader bool   `json:"has_header"` // CSV 首行是否为列名
	NullText  string `json:"null_text"`  // 额外认可的 NULL 写法，默认还认 NULL 与 \N
	Truncate  bool   `json:"truncate"`   // 导入前清空目标表
	MaxErrors int    `json:"max_errors"` // 失败多少行后中止
	ReadOnly  bool   `json:"-"`          // 连接处于只读模式时直接拒绝

	// Guard 供上层做语句黑名单校验，返回 error 表示这条语句不允许执行。
	Guard func(stmt string) error `json:"-"`
}

// ImportResult 是导入结果统计。
type ImportResult struct {
	Format    string   `json:"format"`
	Total     int64    `json:"total"`
	Imported  int64    `json:"imported"`
	Failed    int64    `json:"failed"`
	CostMS    int64    `json:"cost_ms"`
	Truncated bool     `json:"truncated"`
	Messages  []string `json:"messages"`
}

func (r *ImportResult) note(format string, args ...any) {
	if len(r.Messages) >= 30 {
		return
	}
	r.Messages = append(r.Messages, fmt.Sprintf(format, args...))
}

// Import 根据格式分发到 CSV / SQL 导入。
func Import(ctx context.Context, db *sql.DB, src io.Reader, opt ImportOptions) (*ImportResult, error) {
	format := strings.ToLower(strings.TrimSpace(opt.Format))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "sql" {
		return nil, fmt.Errorf("不支持的导入格式：%s（只支持 csv / sql）", opt.Format)
	}
	if opt.ReadOnly {
		return nil, ErrReadOnly
	}
	if opt.MaxErrors <= 0 {
		opt.MaxErrors = 50
	}

	start := time.Now()
	res := &ImportResult{Format: format, Messages: []string{}}
	var err error
	if format == "csv" {
		err = importCSV(ctx, db, src, opt, res)
	} else {
		err = importSQL(ctx, db, src, opt, res)
	}
	res.CostMS = time.Since(start).Milliseconds()
	return res, err
}

// ---------- CSV ----------

// importColumn 是导入需要的最小列信息。
type importColumn struct {
	Name     string
	Type     string
	Nullable bool
}

// loadImportColumns 读取目标表的列，顺序即表定义顺序。
func loadImportColumns(ctx context.Context, db *sql.DB, schema, table string) ([]importColumn, error) {
	if strings.TrimSpace(schema) == "" || strings.TrimSpace(table) == "" {
		return nil, fmt.Errorf("请指定目标库与表")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, schema, table)
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()

	out := make([]importColumn, 0, 16)
	for rows.Next() {
		var c importColumn
		var nullable string
		if err := rows.Scan(&c.Name, &c.Type, &nullable); err != nil {
			return nil, Friendly(err)
		}
		c.Nullable = strings.EqualFold(nullable, "YES")
		c.Type = strings.ToLower(c.Type)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, Friendly(err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("表 %s.%s 不存在，或当前账号看不到它的结构", schema, table)
	}
	return out, nil
}

// isBlankAsNullType 判断该类型在 CSV 里遇到空串时是否应当写入 NULL。
// 数值与日期时间列写空串会被服务端严格模式拒绝，按 NULL 处理更符合直觉。
func isBlankAsNullType(t string) bool {
	switch t {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint",
		"decimal", "numeric", "float", "double", "real", "bit",
		"date", "datetime", "timestamp", "time", "year":
		return true
	}
	return false
}

// importValue 把 CSV 单元格转成可绑定参数。NULL 语义优先于空串。
func importValue(raw string, col importColumn, nullText string) any {
	t := strings.TrimSpace(raw)
	switch {
	case strings.EqualFold(t, "NULL"), t == `\N`:
		return nil
	case nullText != "" && t == nullText:
		return nil
	}
	if t == "" && isBlankAsNullType(col.Type) {
		return nil
	}
	return raw
}

// importCSV 按列映射批量插入。
func importCSV(ctx context.Context, db *sql.DB, src io.Reader, opt ImportOptions, res *ImportResult) error {
	cols, err := loadImportColumns(ctx, db, opt.Schema, opt.Table)
	if err != nil {
		return err
	}
	if opt.Truncate {
		if _, err := db.ExecContext(ctx, "TRUNCATE TABLE "+qualified(opt.Schema, opt.Table)); err != nil {
			return fmt.Errorf("清空表失败：%w", Friendly(err))
		}
		res.note("已清空表 %s.%s", opt.Schema, opt.Table)
	}

	cr := csv.NewReader(src)
	cr.FieldsPerRecord = -1 // 列数交给下面按行校验，报错信息更具体
	cr.LazyQuotes = true
	cr.ReuseRecord = false

	first, err := cr.Read()
	if err == io.EOF {
		return fmt.Errorf("文件是空的")
	}
	if err != nil {
		return fmt.Errorf("读取 CSV 失败：%w", err)
	}
	if len(first) > 0 {
		// 去掉 Excel 另存时可能带上的 BOM
		first[0] = strings.TrimPrefix(first[0], "\ufeff")
	}

	byName := make(map[string]importColumn, len(cols))
	for _, c := range cols {
		byName[strings.ToLower(c.Name)] = c
	}

	var targets []importColumn
	var pending []string
	lineNo := int64(1)
	if opt.HasHeader {
		for i, h := range first {
			name := strings.ToLower(strings.TrimSpace(h))
			c, ok := byName[name]
			if !ok {
				return fmt.Errorf("文件第 %d 列「%s」在表 %s 中不存在", i+1, strings.TrimSpace(h), opt.Table)
			}
			targets = append(targets, c)
		}
	} else {
		if len(first) > len(cols) {
			return fmt.Errorf("文件有 %d 列，多于表的 %d 列，请勾选「首行为列名」或调整文件",
				len(first), len(cols))
		}
		targets = cols[:len(first)]
		pending = first
	}
	if len(targets) == 0 {
		return fmt.Errorf("没有可导入的列")
	}
	seen := map[string]bool{}
	for _, c := range targets {
		if seen[c.Name] {
			return fmt.Errorf("列「%s」在文件里出现了多次", c.Name)
		}
		seen[c.Name] = true
	}

	colList := make([]string, len(targets))
	for i, c := range targets {
		colList[i] = quoteIdent(c.Name)
	}
	oneRow := "(" + strings.TrimSuffix(strings.Repeat("?,", len(targets)), ",") + ")"
	insertOne := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		qualified(opt.Schema, opt.Table), strings.Join(colList, ", "), oneRow)

	// 批失败时逐行重试，避免一行脏数据带走整批
	runBatch := func(batch [][]any, firstLine int64) {
		if len(batch) == 0 {
			return
		}
		ph := make([]string, len(batch))
		args := make([]any, 0, len(batch)*len(targets))
		for i, row := range batch {
			ph[i] = oneRow
			args = append(args, row...)
		}
		stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
			qualified(opt.Schema, opt.Table), strings.Join(colList, ", "), strings.Join(ph, ","))
		if _, err := db.ExecContext(ctx, stmt, args...); err == nil {
			res.Imported += int64(len(batch))
			return
		}
		for i, row := range batch {
			if _, err := db.ExecContext(ctx, insertOne, row...); err != nil {
				res.Failed++
				res.note("第 %d 行：%s", firstLine+int64(i), Friendly(err).Error())
			} else {
				res.Imported++
			}
		}
	}

	batch := make([][]any, 0, importBatchSize)
	batchStart := int64(0)
	stopped := false

	consume := func(record []string, line int64) {
		if stopped {
			return
		}
		res.Total++
		if len(record) != len(targets) {
			res.Failed++
			res.note("第 %d 行：列数 %d 与预期的 %d 不一致，已跳过", line, len(record), len(targets))
			return
		}
		row := make([]any, len(targets))
		for i, raw := range record {
			if strings.TrimSpace(raw) == "" && !targets[i].Nullable && isBlankAsNullType(targets[i].Type) {
				// 非空数值列的空单元格会直接报错，提前给出可读提示
				res.Failed++
				res.note("第 %d 行：列「%s」为非空数值/时间列，不接受空值", line, targets[i].Name)
				return
			}
			row[i] = importValue(raw, targets[i], opt.NullText)
		}
		if len(batch) == 0 {
			batchStart = line
		}
		batch = append(batch, row)
		if len(batch) >= importBatchSize {
			runBatch(batch, batchStart)
			batch = batch[:0]
			if res.Failed > int64(opt.MaxErrors) {
				res.Truncated = true
				res.note("失败行数超过 %d 行，已中止导入", opt.MaxErrors)
				stopped = true
			}
		}
	}

	if pending != nil {
		consume(pending, lineNo)
	}
	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		lineNo++
		if err != nil {
			res.Failed++
			res.note("第 %d 行：%s", lineNo, err.Error())
			if res.Failed > int64(opt.MaxErrors) {
				res.Truncated = true
				break
			}
			continue
		}
		consume(record, lineNo)
	}
	runBatch(batch, batchStart)
	return nil
}

// ---------- SQL ----------

// delimiterStmt 检测 mysqldump 里用于存储过程/触发器的 DELIMITER 指令。
// 面板不做完整的客户端协议解析，遇到就提示改用命令行，避免半截执行。
var delimiterStmt = regexp.MustCompile(`(?im)^\s*DELIMITER\s`)

// importSQL 逐条执行 SQL 文件里的语句。用独立连接执行，文件里的 USE 才能真正生效。
func importSQL(ctx context.Context, db *sql.DB, src io.Reader, opt ImportOptions, res *ImportResult) error {
	raw, err := io.ReadAll(io.LimitReader(src, MaxImportFileBytes+1))
	if err != nil {
		return fmt.Errorf("读取文件失败：%w", err)
	}
	if len(raw) > MaxImportFileBytes {
		return fmt.Errorf("文件超过 %d MB 上限，请拆分后再导入", MaxImportFileBytes>>20)
	}
	text := strings.TrimPrefix(string(raw), "\ufeff")
	if delimiterStmt.MatchString(text) {
		return fmt.Errorf("文件里包含 DELIMITER 指令（通常来自存储过程/触发器），请在服务器上用 mysql 命令行导入")
	}

	stmts := SplitStatements(text)
	if len(stmts) == 0 {
		return fmt.Errorf("文件里没有可执行的语句")
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return Friendly(err)
	}
	defer conn.Close()

	// SQL 文件不一定带 USE，先用面板指定的库做默认（为空则沿用连接配置）
	if s := strings.TrimSpace(opt.Schema); s != "" {
		if _, err := conn.ExecContext(ctx, "USE "+quoteIdent(s)); err != nil {
			return fmt.Errorf("切换到库 %s 失败：%w", s, Friendly(err))
		}
	}

	for i, stmt := range stmts {
		res.Total++
		if opt.Guard != nil {
			if err := opt.Guard(stmt); err != nil {
				res.Failed++
				res.note("第 %d 条：%s", i+1, err.Error())
				continue
			}
		}
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			res.Failed++
			res.note("第 %d 条（%s）：%s", i+1, truncateStatement(stmt, 80), Friendly(err).Error())
			if res.Failed > int64(opt.MaxErrors) {
				res.Truncated = true
				res.note("失败语句超过 %d 条，已中止导入", opt.MaxErrors)
				break
			}
			continue
		}
		res.Imported++
	}
	return nil
}

// truncateStatement 把语句压成一行并截断，便于放进错误消息。
func truncateStatement(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// SplitStatements 把 SQL 文本按分号切成语句。
// 需要正确跳过字符串、反引号标识符、-- / # 行注释与 /* */ 块注释里的分号，
// 否则一条带分号的注释就能把整个文件切碎。
func SplitStatements(src string) []string {
	out := make([]string, 0, 64)
	var b strings.Builder
	var quote byte
	inLine, inBlock := false, false

	flush := func() {
		s := strings.TrimSpace(b.String())
		b.Reset()
		if s == "" {
			return
		}
		// 只有注释没有实体的片段直接丢掉，否则会被当成一条空语句去执行
		if strings.TrimSpace(stripLeadingComments(s)) == "" {
			return
		}
		out = append(out, s)
	}

	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				b.WriteByte('\n')
			}
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
				b.WriteString(" ")
			}
		case quote != 0:
			b.WriteByte(c)
			if c == '\\' && quote != '`' && i+1 < len(src) {
				i++
				b.WriteByte(src[i])
				continue
			}
			if c == quote {
				// 连写两个引号表示字面量引号
				if i+1 < len(src) && src[i+1] == quote {
					i++
					b.WriteByte(src[i])
					continue
				}
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
			b.WriteByte(c)
		case c == '-' && i+1 < len(src) && src[i+1] == '-' &&
			(i+2 >= len(src) || src[i+2] == ' ' || src[i+2] == '\t' || src[i+2] == '\n' || src[i+2] == '\r'):
			inLine = true
			i++
		case c == '#':
			inLine = true
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			i++
		case c == ';':
			flush()
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		flush()
	}
	return out
}
