package mysqldb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// AlterColumnRequest 修改单个字段的定义。
// Column 是改动前的字段名（用于定位），Spec 是改完之后的目标定义（Spec.Name 可与 Column 不同，即改名）。
type AlterColumnRequest struct {
	Schema string     `json:"schema"`
	Table  string     `json:"table"`
	Column string     `json:"column"`
	Spec   ColumnSpec `json:"spec"`
}

// BuildAlterColumn 生成「只改一个字段」的 ALTER 语句。
//
// 和改表向导的区别：向导会把整张表的结构重算一遍并 diff，这里只碰指定字段，
// 其余字段、索引、表选项一概不动 —— 用户在字段页签点一次「修改」，不应该顺手改到别的东西。
// 返回的 ddl 为空且 err 为 nil 表示与现有定义完全一致，没有任何变化。
func BuildAlterColumn(ctx context.Context, db *sql.DB, req AlterColumnRequest) (*AlterColumnPlan, error) {
	schema := strings.TrimSpace(req.Schema)
	table := strings.TrimSpace(req.Table)
	oldName := strings.TrimSpace(req.Column)
	if schema == "" || table == "" || oldName == "" {
		return nil, fmt.Errorf("请指定库、表与字段名")
	}

	defs, err := loadColumnDefs(ctx, db, schema, table)
	if err != nil {
		return nil, err
	}
	var old *columnDef
	for i := range defs {
		if strings.EqualFold(defs[i].Name, oldName) {
			old = &defs[i]
			break
		}
	}
	if old == nil {
		return nil, fmt.Errorf("字段「%s」不存在，可能已被其他会话修改", oldName)
	}
	return buildAlterColumn(schema, table, *old, req.Spec, defs)
}

// buildAlterColumn 是纯逻辑部分：不碰数据库，便于单测。
func buildAlterColumn(schema, table string, old columnDef, spec ColumnSpec, defs []columnDef) (*AlterColumnPlan, error) {
	// 生成列与带 ZEROFILL 这类修饰的列，重新拼定义会把表达式/修饰丢掉，
	// 宁可明确拒绝，也不要静默改掉列的语义
	if strings.Contains(strings.ToUpper(old.Extra), "GENERATED") {
		return nil, fmt.Errorf("字段「%s」是生成列，不能手工修改定义", old.Name)
	}
	if strings.Contains(strings.ToLower(old.Type), "zerofill") {
		return nil, fmt.Errorf("字段「%s」带 ZEROFILL 修饰，本页无法安全重建，请使用「设计」向导", old.Name)
	}

	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = old.Name
	}
	// 没填类型就沿用原类型：前端只改注释/默认值时不必重复填类型
	if strings.TrimSpace(spec.Type) == "" {
		base, arg, unsigned := splitColumnType(old.Type)
		spec.Type, spec.Length, spec.Unsigned = base, arg, unsigned
	}
	// 字符集不显式给出时沿用原列的：MODIFY 时省略 CHARACTER SET 会让列跟随表默认字符集，
	// 原本是 latin1 之类的列会被悄悄转码。
	if charsetTypes[strings.ToLower(strings.TrimSpace(spec.Type))] {
		if strings.TrimSpace(spec.Charset) == "" {
			spec.Charset = old.Charset
		}
		if strings.TrimSpace(spec.Collation) == "" {
			spec.Collation = old.Collation
		}
	}
	if !identName.MatchString(spec.Name) {
		return nil, fmt.Errorf("字段名「%s」只能包含字母、数字、下划线与 $", spec.Name)
	}
	// 改名撞上别的字段：交回服务端报错太晦涩，这里先拦
	for _, d := range defs {
		if !strings.EqualFold(d.Name, old.Name) && strings.EqualFold(d.Name, spec.Name) {
			return nil, fmt.Errorf("字段「%s」已存在，换一个名字", spec.Name)
		}
	}

	body, err := columnDDL(spec)
	if err != nil {
		return nil, err
	}
	oldBody, oldErr := columnDDL(specFromDef(old))
	renamed := !strings.EqualFold(old.Name, spec.Name)
	if oldErr == nil && !renamed && body == oldBody {
		return &AlterColumnPlan{}, nil
	}

	var ddl string
	if renamed {
		ddl = fmt.Sprintf("ALTER TABLE %s CHANGE COLUMN %s %s",
			qualified(schema, table), quoteIdent(old.Name), body)
	} else {
		ddl = fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s", qualified(schema, table), body)
	}

	changes := []string{"改为 " + body}
	if oldErr == nil {
		changes = []string{fmt.Sprintf("%s → %s", oldBody, body)}
	}
	return &AlterColumnPlan{DDL: ddl, Changes: changes, Renamed: renamed}, nil
}

// AlterColumnPlan 是字段修改计划。DDL 为空表示没有任何变化。
type AlterColumnPlan struct {
	DDL     string   `json:"ddl"`
	Changes []string `json:"changes"`
	Renamed bool     `json:"renamed"`
}

// AlterColumn 生成并执行字段修改语句，返回影响行数。
// 返回空 ddl 表示无变化，调用方不必当成错误。
func AlterColumn(ctx context.Context, db *sql.DB, req AlterColumnRequest) (*AlterColumnPlan, int64, error) {
	plan, err := BuildAlterColumn(ctx, db, req)
	if err != nil {
		return nil, 0, err
	}
	if plan.DDL == "" {
		return plan, 0, nil
	}
	res, err := db.ExecContext(ctx, plan.DDL)
	if err != nil {
		return plan, 0, Friendly(err)
	}
	n, _ := res.RowsAffected()
	return plan, n, nil
}

// ---------- 行数据修改 ----------

// PrimaryKey 返回表的主键列（按索引顺序）。没有主键时返回空切片。
func PrimaryKey(ctx context.Context, db *sql.DB, schema, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = 'PRIMARY'
		ORDER BY SEQ_IN_INDEX`, schema, table)
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, Friendly(err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateRowRequest 按主键更新一行。Key 是主键列的旧值，Values 是要改的列与新值。
// 值用 *string：nil 表示 SQL NULL，空串表示真正的空字符串 —— 两者在数据页里必须能区分。
type UpdateRowRequest struct {
	Schema string             `json:"schema"`
	Table  string             `json:"table"`
	Key    map[string]*string `json:"key"`
	Values map[string]*string `json:"values"`
}

// BuildUpdateRow 生成 UPDATE 语句与参数。
//
// 没有主键的表不允许在这里改数据：只能靠「所有列都相等」来定位行，一旦出现重复行
// 就会一次改掉多行，这类事故不能靠 UI 兜底。
func BuildUpdateRow(ctx context.Context, db *sql.DB, req UpdateRowRequest) (string, []any, error) {
	schema := strings.TrimSpace(req.Schema)
	table := strings.TrimSpace(req.Table)
	if schema == "" || table == "" {
		return "", nil, fmt.Errorf("请指定库与表名")
	}
	if len(req.Values) == 0 {
		return "", nil, fmt.Errorf("没有要修改的字段")
	}

	pk, err := PrimaryKey(ctx, db, schema, table)
	if err != nil {
		return "", nil, err
	}
	if len(pk) == 0 {
		return "", nil, fmt.Errorf("表 %s.%s 没有主键，无法唯一定位要修改的行", schema, table)
	}
	defs, err := loadColumnDefs(ctx, db, schema, table)
	if err != nil {
		return "", nil, err
	}
	return buildUpdateRowSQL(schema, table, pk, defs, req)
}

// buildUpdateRowSQL 是纯逻辑部分：不碰数据库，便于单测。
func buildUpdateRowSQL(schema, table string, pk []string, defs []columnDef, req UpdateRowRequest) (string, []any, error) {
	known := make(map[string]string, len(defs))
	generated := make(map[string]bool, len(defs))
	for _, d := range defs {
		known[strings.ToLower(d.Name)] = d.Name
		if strings.Contains(strings.ToUpper(d.Extra), "GENERATED") {
			generated[strings.ToLower(d.Name)] = true
		}
	}

	// 主键值必须齐，少一个就无法唯一定位
	whereParts := make([]string, 0, len(pk))
	whereArgs := make([]any, 0, len(pk))
	for _, col := range pk {
		v, ok := req.Key[col]
		if !ok {
			// 大小写不敏感兜底：前端按 information_schema 的原始列名传，一般不会走到这里
			for k, kv := range req.Key {
				if strings.EqualFold(k, col) {
					v, ok = kv, true
					break
				}
			}
		}
		if !ok || v == nil {
			return "", nil, fmt.Errorf("缺少主键「%s」的值，无法定位行", col)
		}
		whereParts = append(whereParts, quoteIdent(col)+" = ?")
		whereArgs = append(whereArgs, *v)
	}

	setParts := make([]string, 0, len(req.Values))
	setArgs := make([]any, 0, len(req.Values))
	cols := make([]string, 0, len(req.Values))
	for name := range req.Values {
		cols = append(cols, name)
	}
	// 列顺序固定，方便审计日志比对，也避免同样的改动生成不同语句
	sortStringsFold(cols)
	for _, name := range cols {
		real, ok := known[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return "", nil, fmt.Errorf("字段「%s」不存在", name)
		}
		if generated[strings.ToLower(real)] {
			return "", nil, fmt.Errorf("字段「%s」是生成列，只能由数据库计算，不能直接修改", real)
		}
		setParts = append(setParts, quoteIdent(real)+" = ?")
		if v := req.Values[name]; v == nil {
			setArgs = append(setArgs, nil)
		} else {
			setArgs = append(setArgs, *v)
		}
	}

	sqlText := fmt.Sprintf("UPDATE %s SET %s WHERE %s LIMIT 1",
		qualified(schema, table), strings.Join(setParts, ", "), strings.Join(whereParts, " AND "))
	// 参数顺序必须与语句里 ? 的出现顺序一致：SET 在前、WHERE 在后
	args := append(setArgs, whereArgs...)
	return sqlText, args, nil
}

// UpdateRow 按主键更新一行。返回影响行数：0 表示新值与旧值相同，或该行已不存在。
func UpdateRow(ctx context.Context, db *sql.DB, req UpdateRowRequest) (string, int64, error) {
	sqlText, args, err := BuildUpdateRow(ctx, db, req)
	if err != nil {
		return "", 0, err
	}
	res, err := db.ExecContext(ctx, sqlText, args...)
	if err != nil {
		return sqlText, 0, Friendly(err)
	}
	n, _ := res.RowsAffected()
	return sqlText, n, nil
}

// sortStringsFold 按不区分大小写的字典序排序（就地）。
func sortStringsFold(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && strings.ToLower(list[j]) < strings.ToLower(list[j-1]); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
