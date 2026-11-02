package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/mysqldb"
	"warden/internal/secure"
	"warden/internal/store"
)

// mysqlTimeout 单次数据库操作的上限。慢查询再长也不该把页面卡住。
const mysqlTimeout = 30 * time.Second

// ---------- 连接配置（控制端保存，按主机隔离） ----------

func (d *Deps) getMySQLConn(c *gin.Context) {
	hostID := d.hostIDFromQuery(c)
	var conf store.MySQLConf
	if hostID > 0 {
		_ = d.Store.DB.Where("host_id = ?", hostID).First(&conf).Error
	}
	httpx.OK(c, gin.H{
		"host_id":      hostID,
		"host":         conf.Host,
		"port":         conf.Port,
		"username":     conf.Username,
		"database":     conf.Database,
		"charset":      conf.Charset,
		"tls":          conf.TLS,
		"insecure":     conf.Insecure,
		"read_only":    conf.ReadOnly,
		"has_password": conf.Password != "",
		"configured":   strings.TrimSpace(conf.Host) != "",
	})
}

type mysqlConnReq struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	ClearPassword bool   `json:"clear_password"`
	Database      string `json:"database"`
	Charset       string `json:"charset"`
	TLS           bool   `json:"tls"`
	Insecure      bool   `json:"insecure"`
	// 用指针区分「没传」与「传了 false」，新建时默认只读
	ReadOnly *bool `json:"read_only"`
}

func (d *Deps) saveMySQLConn(c *gin.Context) {
	hostID := d.hostIDFromQuery(c)
	if hostID == 0 {
		httpx.BadRequest(c, "主机不存在")
		return
	}
	var req mysqlConnReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}

	var conf store.MySQLConf
	isNew := d.Store.DB.Where("host_id = ?", hostID).First(&conf).Error != nil
	if isNew {
		conf.HostID = hostID
		conf.ReadOnly = true
	}

	addr := strings.TrimSpace(req.Host)
	if addr == "" {
		httpx.BadRequest(c, "请填写数据库地址")
		return
	}
	port := req.Port
	if port == 0 {
		port = mysqldb.DefaultPort
	}
	if port < 1 || port > 65535 {
		httpx.BadRequest(c, "端口应在 1-65535 之间")
		return
	}
	user := strings.TrimSpace(req.Username)
	if user == "" {
		httpx.BadRequest(c, "请填写数据库账号")
		return
	}

	conf.Host = addr
	conf.Port = port
	conf.Username = user
	conf.Database = strings.TrimSpace(req.Database)
	conf.Charset = strings.TrimSpace(req.Charset)
	conf.TLS = req.TLS
	conf.Insecure = req.Insecure && req.TLS
	if req.ReadOnly != nil {
		conf.ReadOnly = *req.ReadOnly
	}

	switch {
	case req.ClearPassword:
		conf.Password = ""
	case strings.TrimSpace(req.Password) != "":
		enc, err := secure.Encrypt(d.Cfg.SecretKey(), req.Password)
		if err != nil {
			httpx.ServerError(c, "凭据加密失败")
			return
		}
		conf.Password = enc
	}

	var err error
	if isNew {
		err = d.Store.DB.Create(&conf).Error
	} else {
		err = d.Store.DB.Save(&conf).Error
	}
	if err != nil {
		httpx.ServerError(c, "保存 MySQL 连接失败")
		return
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("保存 MySQL 连接 %s@%s:%d", conf.Username, conf.Host, conf.Port))
	httpx.OKMsg(c, "已保存", gin.H{
		"host":         conf.Host,
		"port":         conf.Port,
		"username":     conf.Username,
		"database":     conf.Database,
		"charset":      conf.Charset,
		"tls":          conf.TLS,
		"insecure":     conf.Insecure,
		"read_only":    conf.ReadOnly,
		"has_password": conf.Password != "",
		"configured":   true,
	})
}

// ---------- 数据接口（本机直连，远程经 Agent 转发） ----------

// mysqlDB 从请求头还原连接参数（由中间件按主机注入），并按 query 里的 db 临时切库。
// useDefault 为 false 时清空默认库：库表浏览与会话查询的 SQL 全部使用「库.表」限定名，
// 配置里的默认库即使被删或改名，也不该让整个页面连不上。
// 返回的 *sql.DB 由调用方负责 Close。
func (d *Deps) mysqlDB(c *gin.Context, useDefault bool) (*sql.DB, mysqldb.Config, bool) {
	cfg := mysqldb.ConfigFromHeaders(c.Request.Header)
	if cfg.Empty() {
		httpx.Fail(c, http.StatusPreconditionFailed,
			"该主机尚未配置 MySQL 连接，请先在「连接设置」中填写地址与账号")
		return nil, cfg, false
	}
	if !useDefault {
		cfg.Database = ""
	}
	if v := strings.TrimSpace(c.Query("db")); v != "" {
		cfg.Database = v
	}
	db, err := mysqldb.Open(cfg)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return nil, cfg, false
	}
	return db, cfg, true
}

// mysqlCtx 生成带超时的上下文。
func mysqlCtx(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), mysqlTimeout)
}

// mysqlOverview 汇总服务端关键指标，同时兼作连接测试。
// 只查 information_schema 与 SHOW，因此不依赖默认库（配置里的库被删也照样能打开页面）。
func (d *Deps) mysqlOverview(c *gin.Context) {
	db, cfg, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	res, err := mysqldb.Snapshot(ctx, db, cfg)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	res.ReadOnly = cfg.ReadOnly
	httpx.OK(c, res)
}

// mysqlDatabases 列出全部库及体积。
func (d *Deps) mysqlDatabases(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	items, err := mysqldb.ListDatabases(ctx, db)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

// mysqlTables 列出指定库的表。
func (d *Deps) mysqlTables(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	items, err := mysqldb.ListTables(ctx, db, c.Query("schema"), c.Query("q"))
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

// mysqlTable 返回表结构：列、索引与 DDL。
func (d *Deps) mysqlTable(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	detail, err := mysqldb.DescribeTable(ctx, db, c.Query("schema"), c.Query("table"))
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, detail)
}

// mysqlRows 分页读取表数据。
func (d *Deps) mysqlRows(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "50"))
	desc := c.Query("desc") == "1" || c.Query("desc") == "true"

	res, err := mysqldb.PreviewRows(ctx, db, c.Query("schema"), c.Query("table"),
		page, size, c.Query("order"), desc)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, res)
}

// mysqlBlockedKeyword 拦住「一条语句就能搞崩服务或越权」的操作。
// 面板定位是日常运维，账号与权限管理交给 DBA 在服务器上做更安全。
var mysqlBlockedKeyword = map[string]string{
	"shutdown":  "停库命令会直接中断业务",
	"grant":     "面板不提供权限授予",
	"revoke":    "面板不提供权限回收",
	"install":   "会加载服务端插件",
	"uninstall": "会卸载服务端插件",
	"reset":     "会重置主从/日志状态",
}

// mysqlBlockedPair 拦截危险组合，键为「第一关键字 第二关键字」。
var mysqlBlockedPair = map[string]string{
	"create user":     "面板不管理数据库账号",
	"drop user":       "面板不管理数据库账号",
	"alter user":      "面板不管理数据库账号",
	"rename user":     "面板不管理数据库账号",
	"create role":     "面板不管理角色",
	"drop role":       "面板不管理角色",
	"set password":    "面板不修改账号密码",
	"set global":      "修改全局参数可能让服务立刻不可用",
	"set persist":     "会把参数写入配置文件",
	"drop database":   "会整库删除数据，请登录服务器执行",
	"drop schema":     "会整库删除数据，请登录服务器执行",
	"load data":       "会读取服务器本地文件",
	"create database": "建库请登录服务器或使用 SQL 客户端",
}

// checkMySQLStatement 做危险语句拦截。只读模式的拦截在 mysqldb.Execute 里完成。
func checkMySQLStatement(stmt string) error {
	kws := mysqldb.FirstKeywords(stmt, 2)
	if len(kws) == 0 {
		return fmt.Errorf("请输入 SQL")
	}
	if reason, hit := mysqlBlockedKeyword[kws[0]]; hit {
		return fmt.Errorf("面板已禁用 %s：%s，请登录服务器执行",
			strings.ToUpper(kws[0]), reason)
	}
	if len(kws) > 1 {
		// SET @@GLOBAL.xxx 这种写法没有独立的关键字，单独再判一次
		second := kws[1]
		if strings.HasPrefix(second, "@@global") {
			return fmt.Errorf("面板已禁用修改全局参数，请登录服务器执行")
		}
		if reason, hit := mysqlBlockedPair[kws[0]+" "+second]; hit {
			return fmt.Errorf("面板已禁用 %s %s：%s",
				strings.ToUpper(kws[0]), strings.ToUpper(second), reason)
		}
	}
	return nil
}

type mysqlQueryReq struct {
	SQL   string `json:"sql"`
	Limit int    `json:"limit"`
}

// mysqlQuery 执行 SQL 控制台里的一条语句。只读模式下只允许查询类语句。
func (d *Deps) mysqlQuery(c *gin.Context) {
	db, cfg, ok := d.mysqlDB(c, true)
	if !ok {
		return
	}
	defer db.Close()

	var req mysqlQueryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	stmt := strings.TrimSpace(req.SQL)
	if stmt == "" {
		httpx.BadRequest(c, "请输入 SQL")
		return
	}
	if err := checkMySQLStatement(stmt); err != nil {
		httpx.Forbidden(c, err.Error())
		return
	}

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	res, err := mysqldb.Execute(ctx, db, stmt, req.Limit, !cfg.ReadOnly)
	if err != nil {
		if err == mysqldb.ErrReadOnly {
			httpx.Forbidden(c, err.Error())
			return
		}
		// 语法/权限类错误属于正常使用场景，原样回给前端展示
		httpx.OK(c, gin.H{
			"ok":        false,
			"error":     err.Error(),
			"statement": stmt,
		})
		return
	}
	if res.Kind == "exec" {
		middleware.SetAuditDetail(c, fmt.Sprintf("执行 MySQL 写语句 %s（影响 %d 行）",
			strings.ToUpper(strings.Join(mysqldb.FirstKeywords(stmt, 1), " ")), res.RowsAffected))
	}
	httpx.OK(c, gin.H{
		"ok":             true,
		"kind":           res.Kind,
		"columns":        res.Columns,
		"rows":           res.Rows,
		"row_count":      res.RowCount,
		"rows_affected":  res.RowsAffected,
		"last_insert_id": res.LastInsertID,
		"truncated":      res.Truncated,
		"cost_ms":        res.CostMS,
		"statement":      res.Statement,
	})
}

// mysqlSessions 列出当前连接会话。
func (d *Deps) mysqlSessions(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	items, err := mysqldb.ListSessions(ctx, db)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

type mysqlKillReq struct {
	ID int64 `json:"id"`
}

// mysqlKillSession 断开指定会话（相当于 KILL CONNECTION）。
func (d *Deps) mysqlKillSession(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	var req mysqlKillReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	if req.ID <= 0 {
		httpx.BadRequest(c, "会话 ID 不合法")
		return
	}

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	if err := mysqldb.KillSession(ctx, db, req.ID); err != nil {
		middleware.SetAuditDetail(c, fmt.Sprintf("断开 MySQL 会话 #%d 失败", req.ID))
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("断开 MySQL 会话 #%d", req.ID))
	httpx.OKMsg(c, "已断开该会话", gin.H{"id": req.ID})
}

// mysqlVariables 返回全局变量与状态，供「服务信息」页签展示。
// 这里全是 SHOW 语句，同样不需要默认库。
func (d *Deps) mysqlVariables(c *gin.Context) {
	db, _, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	ctx, cancel := mysqlCtx(c)
	defer cancel()

	sections, err := mysqldb.Variables(ctx, db)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{"sections": sections})
}
