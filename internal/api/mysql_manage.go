package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/mysqldb"
)

// mysqlLongTimeout 用于导出与导入：这两类操作天然比单条查询慢，
// 沿用 30s 会把正常的整表导出直接掐断。
const mysqlLongTimeout = 10 * time.Minute

func mysqlLongCtx(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), mysqlLongTimeout)
}

// streamWriter 在第一次真正写出时才设置响应头。
// 这样「查询一开始就失败」的情况还能正常返回 JSON 错误，而不是发一个空的附件。
type streamWriter struct {
	c           *gin.Context
	contentType string
	filename    string
	started     bool
}

func (s *streamWriter) Write(p []byte) (int, error) {
	if !s.started {
		s.started = true
		s.c.Header("Content-Type", s.contentType)
		s.c.Header("Content-Disposition", attachmentHeader(s.filename))
		s.c.Header("Cache-Control", "no-store")
		s.c.Header("X-Content-Type-Options", "nosniff")
	}
	return s.c.Writer.Write(p)
}

// attachmentHeader 生成兼容中文文件名的下载头。
func attachmentHeader(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", ascii, url.PathEscape(name))
}

type mysqlExportReq struct {
	Schema           string `json:"schema"`
	Table            string `json:"table"`
	SQL              string `json:"sql"`
	Format           string `json:"format"`
	BOM              *bool  `json:"bom"`
	NullText         string `json:"null_text"`
	IncludeStructure *bool  `json:"include_structure"`
	IncludeData      *bool  `json:"include_data"`
	Limit            int    `json:"limit"`
}

// mysqlExport 导出表数据或查询结果为 CSV / SQL 附件。
// 用 POST 而不是 GET：SQL 可能很长（URL 放不下），同时能进入审计日志。
func (d *Deps) mysqlExport(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, true)
	if !ok {
		return
	}
	defer db.Close()

	var req mysqlExportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "csv"
	}
	opt := mysqldb.ExportOptions{
		Format:   format,
		NullText: req.NullText,
		Limit:    req.Limit,
	}
	if req.BOM != nil {
		opt.BOM = *req.BOM
	} else {
		opt.BOM = format == "csv"
	}
	if req.IncludeStructure != nil {
		opt.IncludeStructure = *req.IncludeStructure
	}
	if req.IncludeData != nil {
		opt.IncludeData = *req.IncludeData
	}

	contentType := "text/csv; charset=utf-8"
	name := fmt.Sprintf("export_%s.csv", time.Now().Format("20060102_150405"))
	if format == "sql" {
		contentType = "application/sql; charset=utf-8"
		name = fmt.Sprintf("%s.sql", req.Table)
		if req.Schema != "" && req.Table != "" {
			name = fmt.Sprintf("%s.%s.sql", req.Schema, req.Table)
		}
	} else if req.Schema != "" && req.Table != "" {
		name = fmt.Sprintf("%s.%s.csv", req.Schema, req.Table)
	}
	name = sanitizeFilename(name)

	src := mysqldb.ExportSource{Schema: req.Schema, Table: req.Table, SQL: req.SQL}
	ctx, cancel := mysqlLongCtx(c)
	defer cancel()

	sw := &streamWriter{c: c, contentType: contentType, filename: name}
	bw := bufio.NewWriterSize(sw, 32<<10)
	stat, err := mysqldb.Export(ctx, db, src, bw, opt)
	if err == nil {
		err = bw.Flush()
	}
	if err != nil {
		if !sw.started {
			httpx.Fail(c, http.StatusBadGateway, err.Error())
			return
		}
		// 已经开始输出，改不了状态码，只能中断连接并记录
		slog.Warn("MySQL 导出中断", "err", err, "schema", req.Schema, "table", req.Table)
		return
	}
	target := req.Table
	if target == "" {
		target = "查询结果"
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("导出 MySQL %s.%s 为 %s（%d 行 / %d 字节）",
		req.Schema, target, strings.ToUpper(format), stat.Rows, stat.Bytes))
}

// sanitizeFilename 去掉文件系统不接受的字符。
func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_", "\n", "_", "\r", "_")
	return strings.TrimSpace(replacer.Replace(name))
}

// checkMySQLImportStatement 是导入时的语句黑名单。
// 比 SQL 控制台宽松一点：dump 文件里的 CREATE DATABASE / LOCK TABLES 需要放行，
// 但停库、授权、改全局参数这类「一条就出事」的操作依旧拦掉。
func checkMySQLImportStatement(stmt string) error {
	kws := mysqldb.FirstKeywords(stmt, 2)
	if len(kws) == 0 {
		return fmt.Errorf("空语句")
	}
	switch kws[0] {
	case "shutdown", "grant", "revoke", "install", "uninstall", "reset":
		return fmt.Errorf("面板已禁用 %s 语句，请登录服务器执行", strings.ToUpper(kws[0]))
	}
	if len(kws) > 1 {
		if strings.HasPrefix(kws[1], "@@global") {
			return fmt.Errorf("面板已禁用修改全局参数")
		}
		pair := kws[0] + " " + kws[1]
		switch pair {
		case "set global", "set persist":
			return fmt.Errorf("面板已禁用修改全局参数")
		case "load data":
			return fmt.Errorf("面板已禁用 LOAD DATA（会读取服务器本地文件）")
		case "drop database", "drop schema":
			return fmt.Errorf("面板已禁用 DROP DATABASE，请登录服务器执行")
		}
	}
	return nil
}

// mysqlImport 接收 multipart 上传的 CSV / SQL 并写入数据库。
func (d *Deps) mysqlImport(c *gin.Context) {
	db, cfg, ok := d.mysqlDB(c, true)
	if !ok {
		return
	}
	defer db.Close()

	if cfg.ReadOnly {
		httpx.Forbidden(c, "当前连接处于只读模式，如需导入请在「连接设置」中关闭只读")
		return
	}
	if c.ContentType() != "multipart/form-data" {
		httpx.BadRequest(c, "请使用 multipart/form-data 上传文件")
		return
	}

	reader, err := c.Request.MultipartReader()
	if err != nil {
		httpx.BadRequest(c, "解析上传数据失败："+err.Error())
		return
	}

	opt := mysqldb.ImportOptions{
		Format:    c.Query("format"),
		Schema:    c.Query("schema"),
		Table:     c.Query("table"),
		NullText:  c.Query("null_text"),
		HasHeader: c.Query("has_header") != "0",
		Truncate:  c.Query("truncate") == "1",
		Guard:     checkMySQLImportStatement,
	}
	if v, err := strconv.Atoi(c.Query("max_errors")); err == nil {
		opt.MaxErrors = v
	}

	ctx, cancel := mysqlLongCtx(c)
	defer cancel()

	var result *mysqldb.ImportResult
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			httpx.BadRequest(c, "读取上传数据失败："+err.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		result, err = mysqldb.Import(ctx, db, part, opt)
		part.Close()
		if err != nil {
			httpx.BadRequest(c, err.Error())
			return
		}
		break
	}
	if result == nil {
		httpx.BadRequest(c, "没有收到文件")
		return
	}

	target := opt.Table
	if target == "" {
		target = opt.Schema
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("导入 MySQL %s（%s，成功 %d / 失败 %d）",
		target, strings.ToUpper(result.Format), result.Imported, result.Failed))
	httpx.OK(c, result)
}

type mysqlDDLReq struct {
	DryRun bool `json:"dry_run"`
}

// mysqlCreateTable 按向导生成并执行建表语句。
func (d *Deps) mysqlCreateTable(c *gin.Context) {
	var spec mysqldb.TableSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	dryRun := false
	if v, err := strconv.ParseBool(c.Query("dry_run")); err == nil {
		dryRun = v
	}

	ddl, err := mysqldb.BuildCreateTable(spec)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	if dryRun {
		httpx.OK(c, gin.H{"ddl": ddl, "executed": false})
		return
	}

	// 建表语句里的库名是限定名，不依赖连接配置里的默认库
	db, cfg, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()
	if cfg.ReadOnly {
		httpx.Forbidden(c, "当前连接处于只读模式，如需建表请在「连接设置」中关闭只读")
		return
	}

	ctx, cancel := mysqlLongCtx(c)
	defer cancel()
	n, err := mysqldb.ExecDDL(ctx, db, ddl)
	if err != nil {
		middleware.SetAuditDetail(c, "建表失败 "+spec.Schema+"."+spec.Table)
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("创建表 %s.%s（%d 条语句）", spec.Schema, spec.Table, n))
	httpx.OKMsg(c, "表已创建", gin.H{"ddl": ddl, "executed": true, "statements": n})
}

// mysqlAlterTable 与现存结构做 diff，生成并执行 ALTER 语句。
func (d *Deps) mysqlAlterTable(c *gin.Context) {
	var req mysqldb.AlterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	dryRun := false
	if v, err := strconv.ParseBool(c.Query("dry_run")); err == nil {
		dryRun = v
	}

	// 改表同样只用限定名，避免默认库被删后连向导都打不开
	db, cfg, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	ctx, cancel := mysqlLongCtx(c)
	defer cancel()

	plan, err := mysqldb.BuildAlterTable(ctx, db, req)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	if plan.DDL == "" {
		httpx.BadRequest(c, "没有检测到结构变化")
		return
	}
	if dryRun {
		httpx.OK(c, gin.H{"ddl": plan.DDL, "changes": plan.Changes, "executed": false})
		return
	}
	if cfg.ReadOnly {
		httpx.Forbidden(c, "当前连接处于只读模式，如需改表请在「连接设置」中关闭只读")
		return
	}

	n, err := mysqldb.ExecDDL(ctx, db, plan.DDL)
	if err != nil {
		middleware.SetAuditDetail(c, "修改表结构失败 "+req.Schema+"."+req.Table)
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("修改表 %s.%s：%s", req.Schema, req.Table,
		strings.Join(plan.Changes, "；")))
	httpx.OKMsg(c, "表结构已更新", gin.H{
		"ddl": plan.DDL, "changes": plan.Changes, "executed": true, "statements": n,
	})
}

// mysqlAlterColumn 修改单个字段的定义。只碰这一个字段，不像改表向导那样整表 diff。
func (d *Deps) mysqlAlterColumn(c *gin.Context) {
	var req mysqldb.AlterColumnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	dryRun := false
	if v, err := strconv.ParseBool(c.Query("dry_run")); err == nil {
		dryRun = v
	}

	db, cfg, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	ctx, cancel := mysqlLongCtx(c)
	defer cancel()

	// 预览走同一条构建路径，用户看到的 DDL 与实际执行的一定一致
	if dryRun {
		plan, err := mysqldb.BuildAlterColumn(ctx, db, req)
		if err != nil {
			httpx.Fail(c, http.StatusBadGateway, err.Error())
			return
		}
		httpx.OK(c, gin.H{"ddl": plan.DDL, "changes": plan.Changes, "executed": false})
		return
	}
	if cfg.ReadOnly {
		httpx.Forbidden(c, "当前连接处于只读模式，如需改表请在「连接设置」中关闭只读")
		return
	}

	plan, n, err := mysqldb.AlterColumn(ctx, db, req)
	if err != nil {
		middleware.SetAuditDetail(c, "修改字段失败 "+req.Schema+"."+req.Table+"."+req.Column)
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	if plan.DDL == "" {
		httpx.OKMsg(c, "字段定义没有变化", gin.H{"ddl": "", "executed": false})
		return
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("修改字段 %s.%s.%s：%s",
		req.Schema, req.Table, req.Column, strings.Join(plan.Changes, "；")))
	httpx.OKMsg(c, "字段已修改", gin.H{
		"ddl": plan.DDL, "changes": plan.Changes, "executed": true, "affected": n,
	})
}

// mysqlUpdateRow 按主键更新一行数据。
func (d *Deps) mysqlUpdateRow(c *gin.Context) {
	var req mysqldb.UpdateRowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}

	db, cfg, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	if cfg.ReadOnly {
		httpx.Forbidden(c, "当前连接处于只读模式，如需改数据请在「连接设置」中关闭只读")
		return
	}

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	sqlText, n, err := mysqldb.UpdateRow(ctx, db, req)
	if err != nil {
		middleware.SetAuditDetail(c, "修改数据失败 "+req.Schema+"."+req.Table)
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	cols := make([]string, 0, len(req.Values))
	for k := range req.Values {
		cols = append(cols, k)
	}
	sortStringSlice(cols)
	middleware.SetAuditDetail(c, fmt.Sprintf("修改数据 %s.%s（%s）", req.Schema, req.Table,
		strings.Join(cols, ", ")))

	msg := "已更新 1 行"
	if n == 0 {
		// 值没变时 MySQL 也返回 0，不能据此判断行不存在
		msg = "已提交，但没有行发生变化（新值与旧值相同，或该行已被删除）"
	}
	httpx.OKMsg(c, msg, gin.H{"affected": n, "sql": sqlText})
}

// sortStringSlice 就地按字典序排序，让审计日志里的字段顺序稳定。
func sortStringSlice(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// mysqlSlow 返回慢查询聚合与慢日志开关。
func (d *Deps) mysqlSlow(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	minMS, _ := strconv.ParseFloat(c.DefaultQuery("min_ms", "0"), 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	rep, err := mysqldb.SlowQueries(ctx, db, c.Query("schema"), minMS, c.Query("order"), limit)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, rep)
}

type mysqlExplainReq struct {
	SQL string `json:"sql"`
}

// mysqlSlowExplain 对慢查询里的原始 SQL 执行 EXPLAIN，方便直接看执行计划。
func (d *Deps) mysqlSlowExplain(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, true)
	if !ok {
		return
	}
	defer db.Close()

	var req mysqlExplainReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	stmt := strings.TrimSpace(req.SQL)
	if stmt == "" {
		httpx.BadRequest(c, "请提供要分析的 SQL")
		return
	}
	// 「语句模板」里的 ? 无法直接 EXPLAIN，提前给出可读提示
	if strings.Contains(stmt, "?") {
		httpx.BadRequest(c, "这是语句模板（含 ?），请把参数替换成实际值后再分析")
		return
	}
	// EXPLAIN 不改变数据，写语句也能分析；但只接受单条 DML/查询
	kws := mysqldb.FirstKeywords(stmt, 1)
	allowed := map[string]bool{
		"select": true, "insert": true, "update": true, "delete": true,
		"replace": true, "with": true, "table": true,
	}
	if len(kws) == 0 || !allowed[kws[0]] {
		httpx.BadRequest(c, "只能对 SELECT / INSERT / UPDATE / DELETE 语句做执行计划分析")
		return
	}

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	res, err := mysqldb.Execute(ctx, db, "EXPLAIN "+stmt, 200, false)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{
		"columns": res.Columns,
		"rows":    res.Rows,
		"cost_ms": res.CostMS,
	})
}
