package mysqldb

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 单个结果集最多回传的行数，避免一条 SELECT * 把浏览器拖死。
const MaxResultRows = 500

// ---------- 概览 ----------

// Overview 是首页状态条与指标卡的数据。
type Overview struct {
	Online bool   `json:"online"`
	Error  string `json:"error,omitempty"`

	Version        string `json:"version"`
	VersionComment string `json:"version_comment"`
	Hostname       string `json:"hostname"`
	Port           string `json:"port"`
	Uptime         int64  `json:"uptime"`

	ThreadsConnected  int64 `json:"threads_connected"`
	ThreadsRunning    int64 `json:"threads_running"`
	MaxConnections    int64 `json:"max_connections"`
	MaxUsedConnection int64 `json:"max_used_connections"`

	Queries      int64   `json:"queries"`
	QPS          float64 `json:"qps"`
	BytesSent    int64   `json:"bytes_sent"`
	BytesReceive int64   `json:"bytes_received"`
	ComSelect    int64   `json:"com_select"`
	ComInsert    int64   `json:"com_insert"`
	ComUpdate    int64   `json:"com_update"`
	ComDelete    int64   `json:"com_delete"`
	SlowQueries  int64   `json:"slow_queries"`
	AbortedConn  int64   `json:"aborted_connects"`

	BufferPoolSize      int64   `json:"buffer_pool_size"`
	BufferPoolHitRate   float64 `json:"buffer_pool_hit_rate"`
	BufferPoolReadReq   int64   `json:"buffer_pool_read_requests"`
	BufferPoolDiskReads int64   `json:"buffer_pool_reads"`

	DatabaseCount int64 `json:"database_count"`
	TableCount    int64 `json:"table_count"`
	DataSize      int64 `json:"data_size"`
	IndexSize     int64 `json:"index_size"`

	ReadOnly     bool  `json:"read_only"`        // 面板侧配置：只读模式
	ServerReadOn bool  `json:"server_read_only"` // 服务端 read_only 变量
	LatencyMS    int64 `json:"latency_ms"`
}

// Snapshot 采集概览。任何一项失败都尽量降级，而不是整页报错：
// 有些托管实例会限制 information_schema 或部分 SHOW 权限。
func Snapshot(ctx context.Context, db *sql.DB, cfg Config) (*Overview, error) {
	start := time.Now()
	if err := db.PingContext(ctx); err != nil {
		return &Overview{Online: false, Error: Friendly(err).Error()}, nil
	}
	res := &Overview{Online: true, ReadOnly: cfg.ReadOnly}
	res.LatencyMS = time.Since(start).Milliseconds()

	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err == nil {
		res.Version = version
	}

	status, err := loadPairs(ctx, db, "SHOW GLOBAL STATUS")
	if err != nil {
		return res, nil // 没权限看状态也不能让整页挂掉
	}
	vars, _ := loadPairs(ctx, db, "SHOW GLOBAL VARIABLES")

	res.VersionComment = vars["version_comment"]
	res.Hostname = vars["hostname"]
	res.Port = vars["port"]
	res.MaxConnections = atoi64(vars["max_connections"])
	if v := vars["innodb_buffer_pool_size"]; v != "" {
		res.BufferPoolSize = atoi64(v)
	}

	res.Uptime = atoi64(status["Uptime"])
	res.ThreadsConnected = atoi64(status["Threads_connected"])
	res.ThreadsRunning = atoi64(status["Threads_running"])
	res.MaxUsedConnection = atoi64(status["Max_used_connections"])
	res.Queries = atoi64(status["Queries"])
	res.BytesSent = atoi64(status["Bytes_sent"])
	res.BytesReceive = atoi64(status["Bytes_received"])
	res.ComSelect = atoi64(status["Com_select"])
	res.ComInsert = atoi64(status["Com_insert"])
	res.ComUpdate = atoi64(status["Com_update"])
	res.ComDelete = atoi64(status["Com_delete"])
	res.SlowQueries = atoi64(status["Slow_queries"])
	res.AbortedConn = atoi64(status["Aborted_connects"])
	res.BufferPoolReadReq = atoi64(status["Innodb_buffer_pool_read_requests"])
	res.BufferPoolDiskReads = atoi64(status["Innodb_buffer_pool_reads"])
	// 命中率 = 1 - 物理读/逻辑读，磁盘读为 0 时视为 100%
	if res.BufferPoolReadReq > 0 {
		res.BufferPoolHitRate = (1 - float64(res.BufferPoolDiskReads)/float64(res.BufferPoolReadReq)) * 100
	}
	// Queries 是自启动以来的累计值，这里只能给出平均 QPS（瞬时值需要两次采样）
	if res.Uptime > 0 {
		res.QPS = float64(res.Queries) / float64(res.Uptime)
	}
	res.ServerReadOn = strings.EqualFold(vars["read_only"], "ON") ||
		strings.EqualFold(vars["read_only"], "1")

	// 库表规模：information_schema 在部分实例上可能被限制，取不到就把数字留 0
	var tables, dataSize, indexSize int64
	row := db.QueryRowContext(ctx, `SELECT COUNT(*), IFNULL(SUM(DATA_LENGTH),0), IFNULL(SUM(INDEX_LENGTH),0)
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA NOT IN ('information_schema','performance_schema','mysql','sys')`)
	if err := row.Scan(&tables, &dataSize, &indexSize); err == nil {
		res.TableCount = tables
		res.DataSize = dataSize
		res.IndexSize = indexSize
	}
	var dbCount int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.SCHEMATA
		WHERE SCHEMA_NAME NOT IN ('information_schema','performance_schema','mysql','sys')`).Scan(&dbCount); err == nil {
		res.DatabaseCount = dbCount
	}
	return res, nil
}

// loadPairs 把 SHOW 语句的两列表结果读成 map。
func loadPairs(ctx context.Context, db *sql.DB, query string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string, 128)
	for rows.Next() {
		var k, v sql.NullString
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k.String] = v.String
	}
	return out, rows.Err()
}

// ---------- 库 / 表 ----------

// DBStat 是一个库及其体积统计。
type DBStat struct {
	Name      string `json:"name"`
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
	Tables    int64  `json:"tables"`
	Rows      int64  `json:"rows"`
	DataSize  int64  `json:"data_size"`
	IndexSize int64  `json:"index_size"`
	FreeSize  int64  `json:"free_size"`
	System    bool   `json:"system"` // mysql / sys 等系统库
}

// systemSchemas 是通常不需要关注的系统库。
var systemSchemas = map[string]bool{
	"information_schema": true,
	"performance_schema": true,
	"mysql":              true,
	"sys":                true,
}

// ListDatabases 列出全部库及体积。TABLE_ROWS 是估算值（InnoDB 不精确），
// 前端会标注「估算」，避免用户把它当精确行数。
func ListDatabases(ctx context.Context, db *sql.DB) ([]DBStat, error) {
	const q = `
		SELECT s.SCHEMA_NAME,
		       IFNULL(s.DEFAULT_CHARACTER_SET_NAME,''),
		       IFNULL(s.DEFAULT_COLLATION_NAME,''),
		       IFNULL(t.tbl_count,0), IFNULL(t.tbl_rows,0),
		       IFNULL(t.tbl_data,0), IFNULL(t.tbl_idx,0), IFNULL(t.tbl_free,0)
		FROM information_schema.SCHEMATA s
		LEFT JOIN (
			SELECT TABLE_SCHEMA,
			       COUNT(*) AS tbl_count,
			       SUM(IFNULL(TABLE_ROWS,0)) AS tbl_rows,
			       SUM(IFNULL(DATA_LENGTH,0)) AS tbl_data,
			       SUM(IFNULL(INDEX_LENGTH,0)) AS tbl_idx,
			       SUM(IFNULL(DATA_FREE,0)) AS tbl_free
			FROM information_schema.TABLES
			GROUP BY TABLE_SCHEMA
		) t ON t.TABLE_SCHEMA = s.SCHEMA_NAME
		ORDER BY s.SCHEMA_NAME`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()

	var out []DBStat
	for rows.Next() {
		var d DBStat
		if err := rows.Scan(&d.Name, &d.Charset, &d.Collation,
			&d.Tables, &d.Rows, &d.DataSize, &d.IndexSize, &d.FreeSize); err != nil {
			return nil, Friendly(err)
		}
		d.System = systemSchemas[d.Name]
		out = append(out, d)
	}
	return out, rows.Err()
}

// TableStat 是一张表 / 视图的元信息。
type TableStat struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Engine    string `json:"engine"`
	Rows      int64  `json:"rows"`
	DataSize  int64  `json:"data_size"`
	IndexSize int64  `json:"index_size"`
	FreeSize  int64  `json:"free_size"`
	Collation string `json:"collation"`
	Comment   string `json:"comment"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ListTables 列出指定库的表，q 非空时按表名模糊过滤。
func ListTables(ctx context.Context, db *sql.DB, schema, q string) ([]TableStat, error) {
	if strings.TrimSpace(schema) == "" {
		return nil, fmt.Errorf("请指定数据库")
	}
	args := []any{schema}
	sqlText := `
		SELECT TABLE_NAME, TABLE_TYPE, IFNULL(ENGINE,''), IFNULL(TABLE_ROWS,0),
		       IFNULL(DATA_LENGTH,0), IFNULL(INDEX_LENGTH,0), IFNULL(DATA_FREE,0),
		       IFNULL(TABLE_COLLATION,''), IFNULL(TABLE_COMMENT,''),
		       IFNULL(DATE_FORMAT(CREATE_TIME,'%Y-%m-%d %H:%i:%s'),''),
		       IFNULL(DATE_FORMAT(UPDATE_TIME,'%Y-%m-%d %H:%i:%s'),'')
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ?`
	if k := strings.TrimSpace(q); k != "" {
		sqlText += " AND TABLE_NAME LIKE ?"
		args = append(args, "%"+k+"%")
	}
	sqlText += " ORDER BY TABLE_NAME"

	rows, err := db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()

	out := make([]TableStat, 0, 64)
	for rows.Next() {
		var t TableStat
		if err := rows.Scan(&t.Name, &t.Type, &t.Engine, &t.Rows,
			&t.DataSize, &t.IndexSize, &t.FreeSize, &t.Collation, &t.Comment,
			&t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, Friendly(err)
		}
		if t.Type == "VIEW" {
			t.Type = "view"
		} else {
			t.Type = "table"
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Column 是一列的定义。
type Column struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Nullable  bool   `json:"nullable"`
	Key       string `json:"key"`
	Default   string `json:"default"`
	Extra     string `json:"extra"`
	Comment   string `json:"comment"`
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
	// DefaultNull 区分「没有默认值」与「默认值为空串」：
	// information_schema 里两者都是空串，改表向导必须拿到这个信息才不会误删默认值。
	DefaultNull bool `json:"default_null"`
}

// Index 是表上的一条索引（多列合并成一条）。
type Index struct {
	Name        string   `json:"name"`
	Unique      bool     `json:"unique"`
	Type        string   `json:"type"`
	Columns     []string `json:"columns"`
	Cardinality int64    `json:"cardinality"`
}

// TableDetail 是表详情：列、索引、建表语句。
type TableDetail struct {
	Schema    string   `json:"schema"`
	Table     string   `json:"table"`
	Columns   []Column `json:"columns"`
	Indexes   []Index  `json:"indexes"`
	DDL       string   `json:"ddl"`
	Engine    string   `json:"engine"`
	Charset   string   `json:"charset"`
	Collation string   `json:"collation"`
	Comment   string   `json:"comment"`
}

// DescribeTable 读取列定义、索引与 DDL。
func DescribeTable(ctx context.Context, db *sql.DB, schema, table string) (*TableDetail, error) {
	if strings.TrimSpace(schema) == "" || strings.TrimSpace(table) == "" {
		return nil, fmt.Errorf("请指定库与表名")
	}
	res := &TableDetail{Schema: schema, Table: table, Columns: []Column{}, Indexes: []Index{}}

	cols, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, IFNULL(COLUMN_KEY,''),
		       IFNULL(COLUMN_DEFAULT,''), IFNULL(EXTRA,''), IFNULL(COLUMN_COMMENT,''),
		       IFNULL(CHARACTER_SET_NAME,''), IFNULL(COLLATION_NAME,''), COLUMN_DEFAULT IS NULL
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, schema, table)
	if err != nil {
		return nil, Friendly(err)
	}
	defer cols.Close()
	for cols.Next() {
		var c Column
		var nullable string
		var defaultNull int
		if err := cols.Scan(&c.Name, &c.Type, &nullable, &c.Key, &c.Default,
			&c.Extra, &c.Comment, &c.Charset, &c.Collation, &defaultNull); err != nil {
			return nil, Friendly(err)
		}
		c.Nullable = strings.EqualFold(nullable, "YES")
		c.DefaultNull = defaultNull == 1
		res.Columns = append(res.Columns, c)
	}
	if err := cols.Err(); err != nil {
		return nil, Friendly(err)
	}

	idxRows, err := db.QueryContext(ctx, `
		SELECT INDEX_NAME, NON_UNIQUE, IFNULL(COLUMN_NAME,''), IFNULL(INDEX_TYPE,''), IFNULL(CARDINALITY,0)
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY (INDEX_NAME = 'PRIMARY') DESC, INDEX_NAME, SEQ_IN_INDEX`, schema, table)
	if err == nil {
		defer idxRows.Close()
		byName := map[string]*Index{}
		var order []string
		for idxRows.Next() {
			var name, col, typ string
			var nonUnique int
			var card int64
			if err := idxRows.Scan(&name, &nonUnique, &col, &typ, &card); err != nil {
				break
			}
			idx, ok := byName[name]
			if !ok {
				idx = &Index{Name: name, Unique: nonUnique == 0, Type: typ, Cardinality: card}
				byName[name] = idx
				order = append(order, name)
			}
			if col != "" {
				idx.Columns = append(idx.Columns, col)
			}
		}
		for _, n := range order {
			res.Indexes = append(res.Indexes, *byName[n])
		}
	}

	// SHOW CREATE TABLE 是拿到原始 DDL 的唯一可靠方式。
	// 注意视图返回的是 4 列（View / Create View / character_set_client / collation_connection），
	// 表只有 2 列，所以这里按实际列数动态扫描，不能写死两个目标变量。
	res.DDL = showCreate(ctx, db, schema, table)

	// 引擎与字符集给改表向导用：不带回来前端就无法判断「用户到底改没改」
	if eng, coll, cmt, err := loadTableOptions(ctx, db, schema, table); err == nil {
		res.Engine = eng
		res.Collation = coll
		res.Comment = cmt
		if i := strings.IndexByte(coll, '_'); i > 0 {
			res.Charset = coll[:i]
		}
	}
	return res, nil
}

// showCreate 取建表 / 建视图语句，取不到时返回空串。
func showCreate(ctx context.Context, db *sql.DB, schema, table string) string {
	rows, err := db.QueryContext(ctx, "SHOW CREATE TABLE "+qualified(schema, table))
	if err != nil {
		return ""
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil || len(cols) < 2 {
		return ""
	}
	if !rows.Next() {
		return ""
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return ""
	}
	return vals[1].String
}

// ---------- 数据预览 ----------

// RowPage 是表数据的一页。
type RowPage struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Page    int      `json:"page"`
	Size    int      `json:"size"`
	HasMore bool     `json:"has_more"`
	OrderBy string   `json:"order_by"`
	Desc    bool     `json:"desc"`
	CostMS  int64    `json:"cost_ms"`
	// PK 是主键列；为空表示这张表没有主键，前端据此禁用「修改行」
	PK []string `json:"pk"`
}

// PreviewRows 分页读取表数据。
// 不执行 COUNT(*)：大表上它可能扫全表，改为多取一行判断是否还有下一页。
func PreviewRows(ctx context.Context, db *sql.DB, schema, table string, page, size int, orderBy string, desc bool) (*RowPage, error) {
	if strings.TrimSpace(schema) == "" || strings.TrimSpace(table) == "" {
		return nil, fmt.Errorf("请指定库与表名")
	}
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 200 {
		size = 50
	}

	// 只有在列真实存在时才拼接排序列，彻底避免注入
	orderClause := ""
	if ob := strings.TrimSpace(orderBy); ob != "" {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
			schema, table, ob).Scan(&n); err == nil && n > 0 {
			orderClause = " ORDER BY " + quoteIdent(ob)
			if desc {
				orderClause += " DESC"
			}
		}
	}

	q := fmt.Sprintf("SELECT * FROM %s%s LIMIT %d OFFSET %d",
		qualified(schema, table), orderClause, size+1, (page-1)*size)
	start := time.Now()
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()

	cols, data, truncated, err := scanRows(rows, size)
	if err != nil {
		return nil, err
	}
	// 主键取不到不影响浏览，只是前端会禁用行编辑，所以忽略这里的错误
	pk, _ := PrimaryKey(ctx, db, schema, table)
	if pk == nil {
		pk = []string{}
	}
	return &RowPage{
		Columns: cols,
		Rows:    data,
		Page:    page,
		Size:    size,
		HasMore: truncated,
		OrderBy: orderBy,
		Desc:    desc,
		CostMS:  time.Since(start).Milliseconds(),
		PK:      pk,
	}, nil
}

// ---------- SQL 执行 ----------

// Result 是 SQL 控制台的统一返回结构。
type Result struct {
	Kind         string   `json:"kind"` // rows | exec
	Columns      []string `json:"columns"`
	Rows         [][]any  `json:"rows"`
	RowCount     int      `json:"row_count"`
	RowsAffected int64    `json:"rows_affected"`
	LastInsertID int64    `json:"last_insert_id"`
	Truncated    bool     `json:"truncated"`
	CostMS       int64    `json:"cost_ms"`
	Statement    string   `json:"statement"`
}

// readOnlyPrefixes 是可以安全执行（不改变数据）的语句前缀。
var readOnlyPrefixes = []string{
	"select", "show", "desc", "describe", "explain", "with", "table", "values", "help",
}

// IsReadOnlyStatement 判断一条 SQL 是否只读。
func IsReadOnlyStatement(sqlText string) bool {
	k := leadingKeyword(sqlText)
	for _, p := range readOnlyPrefixes {
		if k == p {
			return true
		}
	}
	return false
}

// FirstKeywords 返回语句开头的 n 个关键字（小写），用于识别危险语句。
// 只做粗略切分：调用方关心的是「GRANT ...」「SET GLOBAL ...」这类前缀。
func FirstKeywords(sqlText string, n int) []string {
	s := stripLeadingComments(sqlText)
	fields := strings.Fields(s)
	if len(fields) > n {
		fields = fields[:n]
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, strings.ToLower(strings.Trim(f, "`\"';")))
	}
	return out
}

// stripLeadingComments 去掉语句开头的 /* */ 与 -- / # 行注释。
func stripLeadingComments(sqlText string) string {
	s := strings.TrimSpace(sqlText)
	for {
		switch {
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = strings.TrimSpace(s[i+2:])
				continue
			}
			return ""
		case strings.HasPrefix(s, "--"), strings.HasPrefix(s, "#"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = strings.TrimSpace(s[i+1:])
				continue
			}
			return ""
		}
		return s
	}
}

// leadingKeyword 取首关键字，跳过前置注释与空白。
// 允许最外层括号，例如 (SELECT ...) UNION (SELECT ...)。
func leadingKeyword(sqlText string) string {
	s := stripLeadingComments(sqlText)
	for strings.HasPrefix(s, "(") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "("))
	}
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, " \t\r\n("); i > 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimRight(s, ";"))
}

// Execute 执行单条 SQL。只读语句返回结果集，其余走 Exec 返回影响行数。
func Execute(ctx context.Context, db *sql.DB, sqlText string, limit int, allowWrite bool) (*Result, error) {
	stmt := strings.TrimSpace(sqlText)
	for strings.HasSuffix(stmt, ";") {
		stmt = strings.TrimSpace(strings.TrimSuffix(stmt, ";"))
	}
	if stmt == "" {
		return nil, fmt.Errorf("请输入 SQL")
	}
	if limit <= 0 || limit > MaxResultRows {
		limit = MaxResultRows
	}
	res := &Result{Statement: stmt, Columns: []string{}, Rows: [][]any{}}
	start := time.Now()

	if !IsReadOnlyStatement(stmt) {
		if !allowWrite {
			return nil, ErrReadOnly
		}
		out, err := db.ExecContext(ctx, stmt)
		res.CostMS = time.Since(start).Milliseconds()
		if err != nil {
			return nil, Friendly(err)
		}
		res.Kind = "exec"
		res.RowsAffected, _ = out.RowsAffected()
		res.LastInsertID, _ = out.LastInsertId()
		return res, nil
	}

	rows, err := db.QueryContext(ctx, stmt)
	res.CostMS = time.Since(start).Milliseconds()
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()

	cols, data, truncated, err := scanRows(rows, limit)
	if err != nil {
		return nil, err
	}
	res.Kind = "rows"
	res.Columns = cols
	res.Rows = data
	res.RowCount = len(data)
	res.Truncated = truncated
	return res, nil
}

// scanRows 读取结果集，最多 limit 行，多读一行用来判断是否被截断。
func scanRows(rows *sql.Rows, limit int) ([]string, [][]any, bool, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, false, Friendly(err)
	}
	out := make([][]any, 0, limit)
	truncated := false
	for rows.Next() {
		if len(out) >= limit {
			truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return cols, out, truncated, Friendly(err)
		}
		for i, v := range vals {
			vals[i] = normalizeValue(v)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		return cols, out, truncated, Friendly(err)
	}
	return cols, out, truncated, nil
}

// normalizeValue 把驱动返回的原始值转成 JSON 友好的形式。
func normalizeValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	case time.Time:
		if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
			return t.Format("2006-01-02")
		}
		return t.Format("2006-01-02 15:04:05")
	default:
		return v
	}
}

// ---------- 会话 ----------

// Session 是 SHOW PROCESSLIST 的一行。
type Session struct {
	ID      int64  `json:"id"`
	User    string `json:"user"`
	Host    string `json:"host"`
	DB      string `json:"db"`
	Command string `json:"command"`
	Time    int64  `json:"time"`
	State   string `json:"state"`
	Info    string `json:"info"`
	Self    bool   `json:"self"`
}

// ListSessions 读取当前连接会话。用自己的连接执行，并把自身标记出来防止误杀。
func ListSessions(ctx context.Context, db *sql.DB) ([]Session, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, Friendly(err)
	}
	defer conn.Close()

	var selfID int64
	_ = conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&selfID)

	rows, err := conn.QueryContext(ctx, `
		SELECT ID, IFNULL(USER,''), IFNULL(HOST,''), IFNULL(DB,''),
		       IFNULL(COMMAND,''), IFNULL(TIME,0), IFNULL(STATE,''), IFNULL(INFO,'')
		FROM information_schema.PROCESSLIST
		ORDER BY TIME DESC, ID`)
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()

	out := make([]Session, 0, 32)
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.User, &s.Host, &s.DB,
			&s.Command, &s.Time, &s.State, &s.Info); err != nil {
			return nil, Friendly(err)
		}
		if len(s.Info) > 2000 {
			s.Info = s.Info[:2000] + " …"
		}
		s.Self = s.ID == selfID
		out = append(out, s)
	}
	return out, rows.Err()
}

// KillSession 断开指定会话。KILL 不支持占位符，因此只接受纯数字 ID。
func KillSession(ctx context.Context, db *sql.DB, id int64) error {
	if id <= 0 {
		return fmt.Errorf("会话 ID 不合法")
	}
	if _, err := db.ExecContext(ctx, "KILL CONNECTION "+strconv.FormatInt(id, 10)); err != nil {
		return Friendly(err)
	}
	return nil
}

// ---------- 服务信息 ----------

// Pair 是「键 - 值」一行。
type Pair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Variables 返回全局变量与状态汇总，供「服务信息」页签展示。
func Variables(ctx context.Context, db *sql.DB) (map[string][]Pair, error) {
	out := map[string][]Pair{}
	for _, sec := range []struct{ name, query string }{
		{"variables", "SHOW GLOBAL VARIABLES"},
		{"status", "SHOW GLOBAL STATUS"},
	} {
		m, err := loadPairs(ctx, db, sec.query)
		if err != nil {
			continue
		}
		list := make([]Pair, 0, len(m))
		for k, v := range m {
			list = append(list, Pair{Key: k, Value: v})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
		out[sec.name] = list
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("无法读取服务信息，账号可能缺少 SHOW 权限")
	}
	return out, nil
}

func atoi64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
