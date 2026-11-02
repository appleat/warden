package mysqldb

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// 建表/改表向导只允许这些基础类型。类型名来自用户输入，必须走白名单，
// 否则 `varchar(10)); DROP TABLE x --` 这类内容会被原样拼进 DDL。
var allowedColumnTypes = map[string]bool{
	"tinyint": true, "smallint": true, "mediumint": true, "int": true, "integer": true,
	"bigint": true, "decimal": true, "numeric": true, "float": true, "double": true,
	"real": true, "bit": true, "bool": true, "boolean": true,
	"char": true, "varchar": true, "binary": true, "varbinary": true,
	"tinytext": true, "text": true, "mediumtext": true, "longtext": true,
	"tinyblob": true, "blob": true, "mediumblob": true, "longblob": true,
	"enum": true, "set": true, "json": true,
	"date": true, "datetime": true, "timestamp": true, "time": true, "year": true,
	"geometry": true, "point": true, "linestring": true, "polygon": true,
	"multipoint": true, "multilinestring": true, "multipolygon": true,
	"geometrycollection": true,
}

// 需要显式指定字符集的类型。
var charsetTypes = map[string]bool{
	"char": true, "varchar": true, "tinytext": true, "text": true,
	"mediumtext": true, "longtext": true, "enum": true, "set": true,
}

var (
	// 数值型参数：10 或 10,2
	numericTypeArg = regexp.MustCompile(`^[0-9]{1,6}(,[0-9]{1,6})?$`)
	// enum / set 的参数：'a','b'（允许中文，但不允许括号、分号等能逃出类型定义外的字符）
	enumTypeArg = regexp.MustCompile(`^'(?:[\p{L}\p{N}_\- .:#@/+%]|'')*'(?:,'(?:[\p{L}\p{N}_\- .:#@/+%]|'')*')*$`)
	identName   = regexp.MustCompile(`^[\p{L}\p{N}_$]{1,64}$`)
	optionName  = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	numberText  = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
)

// ColumnSpec 是向导里一列的定义。Default 用指针区分「不设默认值」与「默认值为空串」。
type ColumnSpec struct {
	Name          string  `json:"name"`
	OriginalName  string  `json:"original_name"`
	Type          string  `json:"type"`
	Length        string  `json:"length"`
	Unsigned      bool    `json:"unsigned"`
	Nullable      bool    `json:"nullable"`
	AutoIncrement bool    `json:"auto_increment"`
	Default       *string `json:"default"`
	OnUpdate      string  `json:"on_update"`
	Comment       string  `json:"comment"`
	Charset       string  `json:"charset"`
	Collation     string  `json:"collation"`
	Primary       bool    `json:"primary"`
}

// IndexSpec 是向导里一条索引的定义。
type IndexSpec struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Kind    string   `json:"kind"` // 空 | BTREE | FULLTEXT | SPATIAL
}

// TableSpec 是建表请求。
type TableSpec struct {
	Schema      string       `json:"schema"`
	Table       string       `json:"table"`
	Engine      string       `json:"engine"`
	Charset     string       `json:"charset"`
	Collation   string       `json:"collation"`
	Comment     string       `json:"comment"`
	IfNotExists bool         `json:"if_not_exists"`
	Columns     []ColumnSpec `json:"columns"`
	Indexes     []IndexSpec  `json:"indexes"`
}

// AlterRequest 是改表请求。Columns 是「改完之后」的完整列表，服务端与现存结构做 diff。
type AlterRequest struct {
	Schema    string       `json:"schema"`
	Table     string       `json:"table"`
	Columns   []ColumnSpec `json:"columns"`
	Indexes   []IndexSpec  `json:"indexes"`
	DropExtra bool         `json:"drop_extra"` // 是否真的删除编辑器里去掉的列与索引
	Engine    string       `json:"engine"`
	Charset   string       `json:"charset"`
	Collation string       `json:"collation"`
	Comment   *string      `json:"comment"`
}

// AlterPlan 是改表计划：DDL 可能有多条（索引变更要拆开执行）。
type AlterPlan struct {
	DDL     string   `json:"ddl"`
	Changes []string `json:"changes"`
}

// ---------- 列定义 ----------

// splitColumnType 把 COLUMN_TYPE 拆成「基础类型 + 参数 + 是否无符号」。
func splitColumnType(t string) (base, arg string, unsigned bool) {
	s := strings.TrimSpace(strings.ToLower(t))
	if strings.HasSuffix(s, " unsigned") {
		unsigned = true
		s = strings.TrimSpace(strings.TrimSuffix(s, " unsigned"))
	} else if strings.HasSuffix(s, "unsigned") && strings.Contains(s, " ") {
		unsigned = true
		s = strings.TrimSpace(strings.TrimSuffix(s, "unsigned"))
	}
	if i := strings.IndexByte(s, '('); i >= 0 && strings.HasSuffix(s, ")") {
		base = strings.TrimSpace(s[:i])
		arg = strings.TrimSpace(s[i+1 : len(s)-1])
		return base, arg, unsigned
	}
	return strings.TrimSpace(s), "", unsigned
}

// validateTypeArg 校验类型参数，返回拼好的 `类型(参数)`。
func validateTypeArg(typ, arg string) (string, error) {
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ == "" {
		return "", fmt.Errorf("请选择字段类型")
	}
	if !allowedColumnTypes[typ] {
		return "", fmt.Errorf("不支持的字段类型：%s", typ)
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return typ, nil
	}
	switch typ {
	case "enum", "set":
		if !enumTypeArg.MatchString(arg) {
			return "", fmt.Errorf("%s 的取值应写成 'a','b' 形式", strings.ToUpper(typ))
		}
	default:
		if !numericTypeArg.MatchString(arg) {
			return "", fmt.Errorf("%s 的长度参数只接受数字，如 255 或 10,2", strings.ToUpper(typ))
		}
	}
	return typ + "(" + arg + ")", nil
}

// defaultClause 生成 DEFAULT 片段。nil 表示不写 DEFAULT（交给服务端按可空性处理）。
func defaultClause(c ColumnSpec) (string, error) {
	if c.Default == nil || c.AutoIncrement {
		return "", nil
	}
	v := strings.TrimSpace(*c.Default)
	if v == "" {
		// 留空表示不设置默认值；要「默认空串」请填 ''
		return "", nil
	}
	if v == "''" {
		return " DEFAULT ''", nil
	}
	up := strings.ToUpper(v)
	switch up {
	case "NULL":
		if !c.Nullable {
			return "", fmt.Errorf("字段「%s」是 NOT NULL，默认值不能为 NULL", c.Name)
		}
		return " DEFAULT NULL", nil
	case "CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP()", "NOW()", "NOW":
		return " DEFAULT CURRENT_TIMESTAMP", nil
	case "CURRENT_DATE", "CURRENT_DATE()", "CURDATE()":
		return " DEFAULT CURRENT_DATE", nil
	}
	if numberText.MatchString(v) {
		return " DEFAULT " + v, nil
	}
	return " DEFAULT " + quoteLiteral(v), nil
}

// columnDDL 生成一列的定义（不含列名之外的修饰顺序问题）。
func columnDDL(c ColumnSpec) (string, error) {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return "", fmt.Errorf("字段名不能为空")
	}
	if !identName.MatchString(name) {
		return "", fmt.Errorf("字段名「%s」只能包含字母、数字、下划线与 $", name)
	}
	typ, err := validateTypeArg(c.Type, c.Length)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(quoteIdent(name))
	b.WriteByte(' ')
	b.WriteString(typ)
	if c.Unsigned {
		b.WriteString(" UNSIGNED")
	}
	if charsetTypes[strings.ToLower(strings.TrimSpace(c.Type))] {
		if cs := strings.TrimSpace(c.Charset); cs != "" {
			if !optionName.MatchString(cs) {
				return "", fmt.Errorf("字符集「%s」不合法", cs)
			}
			b.WriteString(" CHARACTER SET " + cs)
		}
		if col := strings.TrimSpace(c.Collation); col != "" {
			if !optionName.MatchString(col) {
				return "", fmt.Errorf("排序规则「%s」不合法", col)
			}
			b.WriteString(" COLLATE " + col)
		}
	}
	if c.Nullable {
		b.WriteString(" NULL")
	} else {
		b.WriteString(" NOT NULL")
	}
	def, err := defaultClause(c)
	if err != nil {
		return "", err
	}
	b.WriteString(def)
	if ou := strings.TrimSpace(c.OnUpdate); ou != "" && !c.AutoIncrement {
		switch strings.ToUpper(ou) {
		case "CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP()", "NOW()":
			b.WriteString(" ON UPDATE CURRENT_TIMESTAMP")
		default:
			return "", fmt.Errorf("ON UPDATE 只支持 CURRENT_TIMESTAMP")
		}
	}
	if c.AutoIncrement {
		b.WriteString(" AUTO_INCREMENT")
	}
	if cm := strings.TrimSpace(c.Comment); cm != "" {
		b.WriteString(" COMMENT " + quoteLiteral(cm))
	}
	return b.String(), nil
}

// indexClause 生成索引定义。
func indexClause(idx IndexSpec, tableColumns map[string]bool) (string, error) {
	cols := make([]string, 0, len(idx.Columns))
	for _, c := range idx.Columns {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !tableColumns[strings.ToLower(c)] {
			return "", fmt.Errorf("索引「%s」引用了不存在的字段：%s", idx.Name, c)
		}
		cols = append(cols, quoteIdent(c))
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("索引「%s」没有选择字段", idx.Name)
	}
	kind := strings.ToUpper(strings.TrimSpace(idx.Kind))
	switch kind {
	case "", "BTREE", "HASH", "FULLTEXT", "SPATIAL":
	default:
		return "", fmt.Errorf("不支持的索引类型：%s", idx.Kind)
	}
	list := strings.Join(cols, ", ")

	if kind == "FULLTEXT" || kind == "SPATIAL" {
		name := strings.TrimSpace(idx.Name)
		if name == "" {
			name = strings.ToLower(kind) + "_" + strings.Join(idx.Columns, "_")
		}
		return fmt.Sprintf("%s KEY %s (%s)", kind, quoteIdent(name), list), nil
	}

	name := strings.TrimSpace(idx.Name)
	prefix := "KEY "
	if idx.Unique {
		prefix = "UNIQUE KEY "
	}
	if name == "" {
		if idx.Unique {
			prefix = "UNIQUE KEY "
		}
		name = "idx_" + strings.Join(idx.Columns, "_")
		if idx.Unique {
			name = "uk_" + strings.Join(idx.Columns, "_")
		}
	}
	if !identName.MatchString(name) {
		return "", fmt.Errorf("索引名「%s」只能包含字母、数字、下划线与 $", name)
	}
	return prefix + quoteIdent(name) + " (" + list + ")", nil
}

// BuildCreateTable 生成建表语句。只拼装、不执行，预览与执行共用同一份结果。
func BuildCreateTable(spec TableSpec) (string, error) {
	table := strings.TrimSpace(spec.Table)
	if table == "" {
		return "", fmt.Errorf("请填写表名")
	}
	if !identName.MatchString(table) {
		return "", fmt.Errorf("表名「%s」只能包含字母、数字、下划线与 $", table)
	}
	if len(spec.Columns) == 0 {
		return "", fmt.Errorf("至少要有一个字段")
	}

	seen := map[string]bool{}
	tableColumns := map[string]bool{}
	defs := make([]string, 0, len(spec.Columns)+len(spec.Indexes)+1)
	primary := make([]string, 0, 2)
	var autoInc string

	for _, c := range spec.Columns {
		key := strings.ToLower(strings.TrimSpace(c.Name))
		if key == "" {
			return "", fmt.Errorf("字段名不能为空")
		}
		if seen[key] {
			return "", fmt.Errorf("字段「%s」重复", c.Name)
		}
		seen[key] = true
		tableColumns[key] = true

		d, err := columnDDL(c)
		if err != nil {
			return "", err
		}
		defs = append(defs, "  "+d)
		if c.Primary {
			primary = append(primary, c.Name)
		}
		if c.AutoIncrement {
			autoInc = c.Name
		}
	}

	for _, idx := range spec.Indexes {
		if strings.EqualFold(strings.TrimSpace(idx.Name), "PRIMARY") {
			for _, c := range idx.Columns {
				if !containsFold(primary, c) {
					primary = append(primary, c)
				}
			}
			continue
		}
		clause, err := indexClause(idx, tableColumns)
		if err != nil {
			return "", err
		}
		defs = append(defs, "  "+clause)
	}

	// AUTO_INCREMENT 必须以索引开头，用户没选主键时自动把自增列设为主键
	if len(primary) == 0 && autoInc != "" {
		primary = append(primary, autoInc)
	}
	if len(primary) > 0 {
		cols := make([]string, len(primary))
		for i, c := range primary {
			if !tableColumns[strings.ToLower(c)] {
				return "", fmt.Errorf("主键引用了不存在的字段：%s", c)
			}
			cols[i] = quoteIdent(c)
		}
		defs = append([]string{"  PRIMARY KEY (" + strings.Join(cols, ", ") + ")"}, defs...)
	}

	opts := []string{}
	if e := strings.TrimSpace(spec.Engine); e != "" {
		if !optionName.MatchString(e) {
			return "", fmt.Errorf("存储引擎「%s」不合法", e)
		}
		opts = append(opts, "ENGINE="+e)
	}
	if cs := strings.TrimSpace(spec.Charset); cs != "" {
		if !optionName.MatchString(cs) {
			return "", fmt.Errorf("字符集「%s」不合法", cs)
		}
		opts = append(opts, "DEFAULT CHARSET="+cs)
	}
	if co := strings.TrimSpace(spec.Collation); co != "" {
		if !optionName.MatchString(co) {
			return "", fmt.Errorf("排序规则「%s」不合法", co)
		}
		opts = append(opts, "COLLATE="+co)
	}
	if cm := strings.TrimSpace(spec.Comment); cm != "" {
		opts = append(opts, "COMMENT="+quoteLiteral(cm))
	}

	head := "CREATE TABLE "
	if spec.IfNotExists {
		head += "IF NOT EXISTS "
	}
	if s := strings.TrimSpace(spec.Schema); s != "" {
		head += qualified(s, table)
	} else {
		head += quoteIdent(table)
	}
	stmt := head + " (\n" + strings.Join(defs, ",\n") + "\n)"
	if len(opts) > 0 {
		stmt += " " + strings.Join(opts, " ")
	}
	return stmt + ";", nil
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// ---------- 现存结构 ----------

// columnDef 是 information_schema 的列定义，比 Column 多保留「默认值是否为 NULL」。
type columnDef struct {
	Name        string
	Type        string
	Nullable    bool
	DefaultNull bool
	Default     string
	Extra       string
	Comment     string
	Charset     string
	Collation   string
}

func loadColumnDefs(ctx context.Context, db *sql.DB, schema, table string) ([]columnDef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, IFNULL(COLUMN_DEFAULT,''), COLUMN_DEFAULT IS NULL,
		       IFNULL(EXTRA,''), IFNULL(COLUMN_COMMENT,''),
		       IFNULL(CHARACTER_SET_NAME,''), IFNULL(COLLATION_NAME,'')
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, schema, table)
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()

	var out []columnDef
	for rows.Next() {
		var d columnDef
		var nullable string
		var defaultNull bool
		if err := rows.Scan(&d.Name, &d.Type, &nullable, &d.Default, &defaultNull,
			&d.Extra, &d.Comment, &d.Charset, &d.Collation); err != nil {
			return nil, Friendly(err)
		}
		d.Nullable = strings.EqualFold(nullable, "YES")
		d.DefaultNull = defaultNull
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, Friendly(err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("表 %s.%s 不存在", schema, table)
	}
	return out, nil
}

// specFromDef 把现存列转成 ColumnSpec，用于与用户编辑后的定义比较。
func specFromDef(d columnDef) ColumnSpec {
	base, arg, unsigned := splitColumnType(d.Type)
	sp := ColumnSpec{
		Name:          d.Name,
		Type:          base,
		Length:        arg,
		Unsigned:      unsigned,
		Nullable:      d.Nullable,
		Comment:       d.Comment,
		Charset:       d.Charset,
		Collation:     d.Collation,
		AutoIncrement: strings.Contains(strings.ToLower(d.Extra), "auto_increment"),
	}
	if !d.DefaultNull {
		v := d.Default
		// 空串默认值用 '' 字面量表示，否则会被 defaultClause 当成「没有默认值」，
		// 改表时就会把原本的 DEFAULT '' 悄悄删掉
		if strings.TrimSpace(v) == "" {
			v = "''"
		}
		sp.Default = &v
	}
	if i := strings.Index(strings.ToLower(d.Extra), "on update "); i >= 0 {
		sp.OnUpdate = strings.TrimSpace(d.Extra[i+len("on update "):])
	}
	return sp
}

// EqualColumnBody 判断两列定义是否等价（忽略列名）。
// 四处空白差异不该触发表结构变更，所以比较的是「生成出来的定义串」。
func EqualColumnBody(a, b ColumnSpec) bool {
	an, bn := a.Name, b.Name
	a.Name, b.Name = "x", "x"
	defer func() { a.Name, b.Name = an, bn }()
	da, err1 := columnDDL(a)
	db, err2 := columnDDL(b)
	return err1 == nil && err2 == nil && da == db
}

// loadTableOptions 读取表当前的引擎、字符集与注释。
func loadTableOptions(ctx context.Context, db *sql.DB, schema, table string) (engine, collation, comment string, err error) {
	row := db.QueryRowContext(ctx, `SELECT IFNULL(ENGINE,''), IFNULL(TABLE_COLLATION,''), IFNULL(TABLE_COMMENT,'')
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, schema, table)
	if e := row.Scan(&engine, &collation, &comment); e != nil {
		return "", "", "", Friendly(e)
	}
	return engine, collation, comment, nil
}

// loadIndexSpecs 读取现存索引，多列合并为一条。
func loadIndexSpecs(ctx context.Context, db *sql.DB, schema, table string) ([]IndexSpec, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT INDEX_NAME, NON_UNIQUE, IFNULL(COLUMN_NAME,''), IFNULL(INDEX_TYPE,'')
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY (INDEX_NAME = 'PRIMARY') DESC, INDEX_NAME, SEQ_IN_INDEX`, schema, table)
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()

	byName := map[string]*IndexSpec{}
	var order []string
	for rows.Next() {
		var name, col, typ string
		var nonUnique int
		if err := rows.Scan(&name, &nonUnique, &col, &typ); err != nil {
			return nil, Friendly(err)
		}
		sp, ok := byName[name]
		if !ok {
			kind := ""
			up := strings.ToUpper(typ)
			if up == "FULLTEXT" || up == "SPATIAL" {
				kind = up
			}
			sp = &IndexSpec{Name: name, Unique: nonUnique == 0, Kind: kind}
			byName[name] = sp
			order = append(order, name)
		}
		if col != "" {
			sp.Columns = append(sp.Columns, col)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, Friendly(err)
	}
	out := make([]IndexSpec, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

// ---------- 改表 ----------

// BuildAlterTable 对比现存结构与目标结构，生成 ALTER 语句。
// 原始结构一律从 information_schema 重新读取，不信任前端回传的「旧值」，
// 否则伪造一份旧结构就能让面板生成任意 DDL。
func BuildAlterTable(ctx context.Context, db *sql.DB, req AlterRequest) (*AlterPlan, error) {
	if strings.TrimSpace(req.Schema) == "" || strings.TrimSpace(req.Table) == "" {
		return nil, fmt.Errorf("请指定库与表")
	}
	if len(req.Columns) == 0 {
		return nil, fmt.Errorf("至少要保留一个字段")
	}
	origDefs, err := loadColumnDefs(ctx, db, req.Schema, req.Table)
	if err != nil {
		return nil, err
	}
	origByName := make(map[string]columnDef, len(origDefs))
	origOrder := make([]string, len(origDefs))
	for i, d := range origDefs {
		origByName[strings.ToLower(d.Name)] = d
		origOrder[i] = d.Name
	}

	clauses := make([]string, 0, 8)
	changes := make([]string, 0, 8)
	consumed := map[string]bool{}
	targets := map[string]bool{}

	for _, c := range req.Columns {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return nil, fmt.Errorf("字段名不能为空")
		}
		if targets[strings.ToLower(name)] {
			return nil, fmt.Errorf("字段「%s」重复", name)
		}
		targets[strings.ToLower(name)] = true

		lookup := name
		if s := strings.TrimSpace(c.OriginalName); s != "" {
			lookup = s
		}
		old, exists := origByName[strings.ToLower(lookup)]
		if !exists {
			d, err := columnDDL(c)
			if err != nil {
				return nil, err
			}
			clauses = append(clauses, "ADD COLUMN "+d)
			changes = append(changes, "新增字段 "+name)
			continue
		}
		consumed[strings.ToLower(old.Name)] = true

		want := normalizeDesired(c, old)
		if EqualColumnBody(specFromDef(old), want) {
			continue
		}
		d, err := columnDDL(want)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(old.Name, name) {
			clauses = append(clauses, "MODIFY COLUMN "+d)
			changes = append(changes, "修改字段 "+name)
		} else {
			clauses = append(clauses, "CHANGE COLUMN "+quoteIdent(old.Name)+" "+d)
			changes = append(changes, "重命名字段 "+old.Name+" → "+name)
		}
	}

	if req.DropExtra {
		for _, name := range origOrder {
			if consumed[strings.ToLower(name)] {
				continue
			}
			clauses = append(clauses, "DROP COLUMN "+quoteIdent(name))
			changes = append(changes, "删除字段 "+name)
		}
	}

	// 表选项
	curEngine, curCollation, curComment, err := loadTableOptions(ctx, db, req.Schema, req.Table)
	if err == nil {
		if e := strings.TrimSpace(req.Engine); e != "" && !strings.EqualFold(e, curEngine) {
			if !optionName.MatchString(e) {
				return nil, fmt.Errorf("存储引擎「%s」不合法", e)
			}
			clauses = append(clauses, "ENGINE="+e)
			changes = append(changes, fmt.Sprintf("存储引擎 %s → %s", curEngine, e))
		}
		if cs := strings.TrimSpace(req.Charset); cs != "" {
			current := strings.SplitN(curCollation, "_", 2)[0]
			if !strings.EqualFold(cs, current) {
				if !optionName.MatchString(cs) {
					return nil, fmt.Errorf("字符集「%s」不合法", cs)
				}
				clauses = append(clauses, "DEFAULT CHARSET="+cs)
				changes = append(changes, fmt.Sprintf("字符集 %s → %s", current, cs))
			}
		}
		if co := strings.TrimSpace(req.Collation); co != "" && !strings.EqualFold(co, curCollation) {
			if !optionName.MatchString(co) {
				return nil, fmt.Errorf("排序规则「%s」不合法", co)
			}
			clauses = append(clauses, "COLLATE="+co)
			changes = append(changes, fmt.Sprintf("排序规则 %s → %s", curCollation, co))
		}
		if req.Comment != nil && strings.TrimSpace(*req.Comment) != curComment {
			clauses = append(clauses, "COMMENT="+quoteLiteral(*req.Comment))
			changes = append(changes, "更新表注释")
		}
	}

	stmts := make([]string, 0, 4)
	if len(clauses) > 0 {
		stmts = append(stmts, "ALTER TABLE "+qualified(req.Schema, req.Table)+"\n  "+
			strings.Join(clauses, ",\n  ")+";")
	}

	// 索引变更单独成句：MySQL 不允许在一条 ALTER 里先 DROP 再 ADD 同名索引
	if req.Indexes != nil {
		idxStmts, idxChanges, err := alterIndexes(ctx, db, req)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, idxStmts...)
		changes = append(changes, idxChanges...)
	}

	return &AlterPlan{DDL: strings.Join(stmts, "\n"), Changes: changes}, nil
}

// normalizeDesired 用现存列补全用户没填的项，避免「没填字符集」被误判成一次修改。
func normalizeDesired(c ColumnSpec, old columnDef) ColumnSpec {
	out := c
	oldSpec := specFromDef(old)
	if strings.TrimSpace(out.Type) == "" {
		out.Type = oldSpec.Type
	}
	if strings.TrimSpace(out.Length) == "" && out.Type == oldSpec.Type {
		out.Length = oldSpec.Length
	}
	if charsetTypes[strings.ToLower(out.Type)] {
		if strings.TrimSpace(out.Charset) == "" {
			out.Charset = oldSpec.Charset
		}
		if strings.TrimSpace(out.Collation) == "" {
			out.Collation = oldSpec.Collation
		}
	}
	// 前端没提供 ON UPDATE 时保留原样，否则一次「只改注释」会把自动更新时间去掉
	if strings.TrimSpace(out.OnUpdate) == "" && oldSpec.OnUpdate != "" {
		out.OnUpdate = oldSpec.OnUpdate
	}
	return out
}

// alterIndexes 处理索引增删改。
func alterIndexes(ctx context.Context, db *sql.DB, req AlterRequest) ([]string, []string, error) {
	orig, err := loadIndexSpecs(ctx, db, req.Schema, req.Table)
	if err != nil {
		return nil, nil, err
	}
	origByName := make(map[string]IndexSpec, len(orig))
	for _, idx := range orig {
		origByName[strings.ToLower(idx.Name)] = idx
	}

	tableColumns := map[string]bool{}
	defs, err := loadColumnDefs(ctx, db, req.Schema, req.Table)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range defs {
		tableColumns[strings.ToLower(d.Name)] = true
	}
	for _, c := range req.Columns {
		tableColumns[strings.ToLower(c.Name)] = true
	}

	var stmts, changes []string
	seen := map[string]bool{}
	table := qualified(req.Schema, req.Table)

	for _, want := range req.Indexes {
		name := strings.TrimSpace(want.Name)
		isPrimary := strings.EqualFold(name, "PRIMARY")
		key := strings.ToLower(name)
		if !isPrimary {
			if key == "" {
				return nil, nil, fmt.Errorf("索引名不能为空")
			}
			if seen[key] {
				return nil, nil, fmt.Errorf("索引「%s」重复", name)
			}
			seen[key] = true
		}

		if have, ok := origByName[key]; ok {
			if sameIndex(have, want) {
				continue
			}
			drop := "ALTER TABLE " + table + " DROP PRIMARY KEY;"
			if !isPrimary {
				drop = "ALTER TABLE " + table + " DROP INDEX " + quoteIdent(have.Name) + ";"
			}
			stmts = append(stmts, drop)
			changes = append(changes, "删除索引 "+have.Name)
		}

		if isPrimary {
			cols := make([]string, 0, len(want.Columns))
			for _, c := range want.Columns {
				if !tableColumns[strings.ToLower(c)] {
					return nil, nil, fmt.Errorf("主键引用了不存在的字段：%s", c)
				}
				cols = append(cols, quoteIdent(c))
			}
			if len(cols) == 0 {
				return nil, nil, fmt.Errorf("主键没有选择字段")
			}
			stmts = append(stmts, "ALTER TABLE "+table+" ADD PRIMARY KEY ("+strings.Join(cols, ", ")+");")
			changes = append(changes, "重建主键")
			continue
		}
		clause, err := indexClause(want, tableColumns)
		if err != nil {
			return nil, nil, err
		}
		stmts = append(stmts, "ALTER TABLE "+table+" ADD "+clause+";")
		changes = append(changes, "新增索引 "+name)
	}

	if req.DropExtra {
		for _, idx := range orig {
			if seen[strings.ToLower(idx.Name)] || strings.EqualFold(idx.Name, "PRIMARY") {
				continue
			}
			kept := false
			for _, want := range req.Indexes {
				if strings.EqualFold(want.Name, idx.Name) {
					kept = true
					break
				}
			}
			if kept {
				continue
			}
			stmts = append(stmts, "ALTER TABLE "+table+" DROP INDEX "+quoteIdent(idx.Name)+";")
			changes = append(changes, "删除索引 "+idx.Name)
		}
	}
	return stmts, changes, nil
}

// sameIndex 比较两条索引的定义是否一致。
func sameIndex(a, b IndexSpec) bool {
	if a.Unique != b.Unique || !strings.EqualFold(a.Kind, b.Kind) {
		return false
	}
	if len(a.Columns) != len(b.Columns) {
		return false
	}
	for i := range a.Columns {
		if !strings.EqualFold(a.Columns[i], b.Columns[i]) {
			return false
		}
	}
	return true
}

// ExecDDL 执行向导生成的 DDL（可能多条，逐条执行）。
// 用同一个连接保证多条语句落在同一个会话里。
func ExecDDL(ctx context.Context, db *sql.DB, ddl string) (int, error) {
	stmts := SplitStatements(ddl)
	if len(stmts) == 0 {
		return 0, fmt.Errorf("没有需要执行的语句")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, Friendly(err)
	}
	defer conn.Close()

	done := 0
	for i, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return done, fmt.Errorf("第 %d 条语句失败：%s", i+1, Friendly(err).Error())
		}
		done++
	}
	return done, nil
}
