// Package mysqltest 提供一个假的 MySQL 驱动，用来在没有真实数据库的环境里
// 验证导出 / 备份逻辑。只被测试引用，不会进入正式产物。
package mysqltest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
)

// Table 是一张假表。
type Table struct {
	Kind  string // BASE TABLE | VIEW
	DDL   string
	Cols  []string
	Types []string
	Rows  [][]driver.Value
}

// Server 是一台假 MySQL。
type Server struct {
	Schema    string
	Charset   string
	Collation string
	// Strict 打开后按真实 MySQL 的行为挑错：SELECT 的表名没带库限定、
	// 而连接上又没有默认库时，返回 1046 No database selected。
	Strict bool

	mu      sync.Mutex
	order   []string
	tables  map[string]*Table
	execs   []string
	queries []string
}

// New 创建一台假 MySQL，默认字符集 utf8mb4。
func New(schema string) *Server {
	return &Server{
		Schema:    schema,
		Charset:   "utf8mb4",
		Collation: "utf8mb4_general_ci",
		tables:    map[string]*Table{},
	}
}

// AddTable 添加一张基础表。
func (s *Server) AddTable(name, ddl string, cols, types []string, rows [][]driver.Value) {
	s.add(name, &Table{Kind: "BASE TABLE", DDL: ddl, Cols: cols, Types: types, Rows: rows})
}

// AddView 添加一个视图（导出时应被跳过）。
func (s *Server) AddView(name, ddl string) {
	s.add(name, &Table{Kind: "VIEW", DDL: ddl})
}

func (s *Server) add(name string, t *Table) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tables[name]; !ok {
		s.order = append(s.order, name)
	}
	s.tables[name] = t
}

// DB 返回指向这台假 MySQL 的连接池。
func (s *Server) DB() *sql.DB { return sql.OpenDB(&connector{s: s}) }

// Execs 返回执行过的非查询语句（用于断言是否尝试开启快照）。
func (s *Server) Execs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.execs))
	copy(out, s.execs)
	return out
}

// ---------- driver 实现 ----------

type connector struct{ s *Server }

func (c *connector) Driver() driver.Driver { return drv{} }
func (c *connector) Connect(context.Context) (driver.Conn, error) {
	return &conn{s: c.s}, nil
}

type drv struct{}

func (drv) Open(string) (driver.Conn, error) { return nil, io.EOF }

type conn struct{ s *Server }

func (c *conn) Prepare(string) (driver.Stmt, error) { return nil, io.EOF }
func (c *conn) Close() error                        { return nil }
func (c *conn) Begin() (driver.Tx, error)           { return nil, io.EOF }

func (c *conn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.s.mu.Lock()
	c.s.execs = append(c.s.execs, q)
	c.s.mu.Unlock()
	return driver.RowsAffected(0), nil
}

func (c *conn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.s.mu.Lock()
	c.s.queries = append(c.s.queries, q)
	c.s.mu.Unlock()

	up := strings.ToUpper(q)
	switch {
	case strings.Contains(up, "FROM INFORMATION_SCHEMA.TABLES"):
		if len(args) > 0 && args[0].Value != c.s.Schema {
			return &rows{cols: []string{"TABLE_NAME", "TABLE_TYPE"}}, nil
		}
		out := make([][]driver.Value, 0, len(c.s.order))
		for _, name := range c.s.order {
			out = append(out, []driver.Value{name, c.s.tables[name].Kind})
		}
		return &rows{cols: []string{"TABLE_NAME", "TABLE_TYPE"}, data: out}, nil

	case strings.Contains(up, "FROM INFORMATION_SCHEMA.SCHEMATA"):
		if len(args) > 0 && args[0].Value != c.s.Schema {
			return &rows{cols: []string{"CHARSET", "COLLATION"}}, nil
		}
		return &rows{
			cols: []string{"CHARSET", "COLLATION"},
			data: [][]driver.Value{{c.s.Charset, c.s.Collation}},
		}, nil

	case strings.HasPrefix(up, "SHOW CREATE TABLE"):
		name, _ := tableRef(q)
		t, ok := c.s.tables[name]
		if !ok {
			return nil, io.EOF
		}
		return &rows{
			cols: []string{"Table", "Create Table"},
			data: [][]driver.Value{{name, t.DDL}},
		}, nil

	case strings.HasPrefix(up, "SELECT * FROM"):
		name, qualified := tableRef(q)
		// 真实 MySQL 没有默认库时，未限定的表名会直接报 1046
		if c.s.Strict && !qualified {
			return nil, errors.New("Error 1046 (3D000): No database selected")
		}
		t, ok := c.s.tables[name]
		if !ok {
			return nil, io.EOF
		}
		cols := make([]string, len(t.Cols))
		copy(cols, t.Cols)
		return &rows{cols: cols, data: t.Rows, types: t.Types}, nil
	}
	return nil, io.EOF
}

// tableRef 从 SELECT ... FROM / SHOW CREATE TABLE 里抠出表名，
// 并说明它是否带了库限定名（`db`.`tbl`）。
func tableRef(q string) (name string, qualified bool) {
	up := strings.ToUpper(q)
	var start int
	switch {
	case strings.Contains(up, " FROM "):
		start = strings.LastIndex(up, " FROM ") + len(" FROM ")
	case strings.Contains(up, "TABLE "):
		start = strings.LastIndex(up, "TABLE ") + len("TABLE ")
	default:
		return "", false
	}
	rest := strings.TrimSpace(q[start:])
	if k := strings.LastIndex(rest, "."); k >= 0 {
		qualified = true
		rest = rest[k+1:]
	}
	return strings.Trim(rest, "` "), qualified
}

type rows struct {
	cols  []string
	data  [][]driver.Value
	types []string
	idx   int
}

func (r *rows) Columns() []string { return r.cols }
func (r *rows) Close() error      { return nil }
func (r *rows) Next(dest []driver.Value) error {
	if r.idx >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.idx])
	r.idx++
	return nil
}

// ColumnTypeDatabaseTypeName 让驱动把列类型带回来（决定数值是否加引号）。
func (r *rows) ColumnTypeDatabaseTypeName(i int) string {
	if i < len(r.types) {
		return r.types[i]
	}
	return ""
}
