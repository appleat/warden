package api

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"warden/internal/backup"
	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/mysqldb"
	"warden/internal/store"
)

// mysqlBackupView 把任务转成前端要的形状：计划描述、范围、运行态都在这里算好，
// 前端不必重复实现一套「每周三 01:00」的拼装逻辑。
func (d *Deps) mysqlBackupView(t *store.MySQLBackup) gin.H {
	scope := "full"
	// 整库任务在库里存的是空串，接口统一回空数组，前端少一次判空
	tables := t.Tables
	if tables == nil {
		tables = []string{}
	}
	if len(tables) > 0 {
		scope = "tables"
	}
	running := d.Backup != nil && d.Backup.Running(t.ID)
	status := t.LastStatus
	if running {
		status = "running"
	}
	view := gin.H{
		"id":          t.ID,
		"host_id":     t.HostID,
		"name":        t.Name,
		"schema":      t.Schema,
		"tables":      tables,
		"scope":       scope,
		"table_count": len(tables),
		"freq":        t.Freq,
		"hour":        t.Hour,
		"minute":      t.Minute,
		"weekday":     t.Weekday,
		"month_day":   t.MonthDay,
		"schedule":    backup.Describe(t),
		"keep":        t.Keep,
		"compress":    t.Compress,
		"enabled":     t.Enabled,
		"next_run_at": t.NextRunAt,
		"last_run_at": t.LastRunAt,
		"last_status": status,
		"last_error":  t.LastError,
		"last_file":   t.LastFile,
		"last_size":   t.LastSize,
		"last_rows":   t.LastRows,
		"created_at":  t.CreatedAt,
	}
	if t.LastRunAt != nil {
		view["last_run_text"] = t.LastRunAt.Format("2006-01-02 15:04:05")
	}
	if t.NextRunAt != nil && t.Enabled {
		view["next_run_text"] = t.NextRunAt.Format("2006-01-02 15:04")
	}
	return view
}

// listMySQLBackups 列出当前主机的备份任务。
func (d *Deps) listMySQLBackups(c *gin.Context) {
	hostID := d.hostIDFromQuery(c)
	var list []store.MySQLBackup
	q := d.Store.DB.Order("id")
	if hostID > 0 {
		q = q.Where("host_id = ?", hostID)
	}
	if err := q.Find(&list).Error; err != nil {
		httpx.ServerError(c, "读取备份任务失败")
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, d.mysqlBackupView(&list[i]))
	}
	dir := ""
	if d.Backup != nil {
		dir = d.Backup.Root()
	}
	httpx.OK(c, gin.H{"items": out, "dir": dir})
}

type mysqlBackupReq struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
	// Tables 用指针区分「没传」与「传了空数组」：
	// 传空数组 = 改成整库备份；没传 = 保持原有选择，避免局部更新把选表悄悄清掉
	Tables   *[]string `json:"tables"`
	Freq     string    `json:"freq"`
	Hour     int       `json:"hour"`
	Minute   int       `json:"minute"`
	Weekday  int       `json:"weekday"`
	MonthDay int       `json:"month_day"`
	// Keep 用指针：0 表示「不自动清理」，不能被当成「没传」而套上默认值
	Keep     *int  `json:"keep"`
	Compress *bool `json:"compress"`
	Enabled  *bool `json:"enabled"`
}

// applyBackupReq 把请求字段套到任务上，返回可读的错误。
func applyBackupReq(t *store.MySQLBackup, req *mysqlBackupReq) error {
	schema := strings.TrimSpace(req.Schema)
	if schema == "" {
		return fmt.Errorf("请选择要备份的数据库")
	}

	if req.Tables != nil {
		// 表名去空、去重，顺手挡掉明显不合理的输入
		tables := make([]string, 0, len(*req.Tables))
		seen := map[string]bool{}
		for _, name := range *req.Tables {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			if strings.ContainsRune(name, 0) || len(name) > 64 {
				return fmt.Errorf("表名不合法：%s", name)
			}
			seen[name] = true
			tables = append(tables, name)
		}
		if len(tables) > 2000 {
			return fmt.Errorf("一次最多选择 2000 张表，整库备份请清空表选择")
		}
		t.Tables = tables
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		// 修改时没带名称就保留原名，只有新建才按库名兜底
		if t.Name == "" {
			name = schema + " 备份"
		} else {
			name = t.Name
		}
	}
	if len([]rune(name)) > 64 {
		return fmt.Errorf("任务名称不能超过 64 个字")
	}

	t.Name = name
	t.Schema = schema
	t.Freq = req.Freq
	t.Hour = req.Hour
	t.Minute = req.Minute
	t.Weekday = req.Weekday
	t.MonthDay = req.MonthDay
	if req.Keep != nil {
		t.Keep = *req.Keep
	}
	if req.Compress != nil {
		t.Compress = *req.Compress
	}
	if req.Enabled != nil {
		t.Enabled = *req.Enabled
	}
	backup.Normalize(t)
	return nil
}

// saveMySQLBackup 新建（POST）或修改（PUT /:id）备份任务。
func (d *Deps) saveMySQLBackup(c *gin.Context) {
	if d.Backup == nil {
		httpx.ServerError(c, "备份服务未启动")
		return
	}
	var req mysqlBackupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}

	var task store.MySQLBackup
	idRaw := c.Param("id")
	isNew := idRaw == ""
	if !isNew {
		id, err := strconv.ParseUint(idRaw, 10, 64)
		if err != nil {
			httpx.BadRequest(c, "任务 ID 不合法")
			return
		}
		if err := d.Store.DB.First(&task, uint(id)).Error; err != nil {
			httpx.NotFound(c, "备份任务不存在")
			return
		}
	} else {
		hostID := d.hostIDFromQuery(c)
		if hostID == 0 {
			httpx.BadRequest(c, "主机不存在")
			return
		}
		task.HostID = hostID
		task.Compress = true
		task.Enabled = true
		task.Keep = 7
		task.Hour = 2
	}

	// 新建时前端可能不传 compress/enabled，用默认值兜底
	if err := applyBackupReq(&task, &req); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	if isNew {
		if req.Compress == nil {
			task.Compress = true
		}
		if req.Enabled == nil {
			task.Enabled = true
		}
	}

	task.NextRunAt = nextRunPtr(&task)

	var err error
	if isNew {
		err = d.Store.DB.Create(&task).Error
	} else {
		err = d.Store.DB.Save(&task).Error
	}
	if err != nil {
		httpx.ServerError(c, "保存备份任务失败")
		return
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("保存 MySQL 备份任务「%s」：%s.%s，%s",
		task.Name, task.Schema, scopeText(&task), backup.Describe(&task)))
	httpx.OKMsg(c, "已保存", d.mysqlBackupView(&task))
}

func scopeText(t *store.MySQLBackup) string {
	if len(t.Tables) == 0 {
		return "整库"
	}
	if len(t.Tables) <= 3 {
		return strings.Join(t.Tables, ",")
	}
	return fmt.Sprintf("%d 张表", len(t.Tables))
}

func nextRunPtr(t *store.MySQLBackup) *time.Time {
	if !t.Enabled {
		// 停用的任务不排期，避免重新启用时「补跑」一堆过期任务
		return nil
	}
	next := backup.NextRun(t, time.Now())
	return &next
}

// toggleMySQLBackup 启用 / 停用任务。
func (d *Deps) toggleMySQLBackup(c *gin.Context) {
	var task store.MySQLBackup
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "任务 ID 不合法")
		return
	}
	if err := d.Store.DB.First(&task, uint(id)).Error; err != nil {
		httpx.NotFound(c, "备份任务不存在")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		httpx.BadRequest(c, "缺少 enabled 参数")
		return
	}
	task.Enabled = *req.Enabled
	task.NextRunAt = nextRunPtr(&task)
	if err := d.Store.DB.Model(&store.MySQLBackup{}).Where("id = ?", task.ID).
		Updates(map[string]any{"enabled": task.Enabled, "next_run_at": task.NextRunAt}).Error; err != nil {
		httpx.ServerError(c, "更新失败")
		return
	}
	state := "已启用"
	if !task.Enabled {
		state = "已停用"
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("%s MySQL 备份任务「%s」", state, task.Name))
	httpx.OKMsg(c, state, gin.H{"enabled": task.Enabled, "next_run_at": task.NextRunAt})
}

// deleteMySQLBackup 删除任务，同时清掉它的备份文件。
func (d *Deps) deleteMySQLBackup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "任务 ID 不合法")
		return
	}
	var task store.MySQLBackup
	if err := d.Store.DB.First(&task, uint(id)).Error; err != nil {
		httpx.NotFound(c, "备份任务不存在")
		return
	}
	if d.Backup != nil && d.Backup.Running(task.ID) {
		httpx.BadRequest(c, "该任务正在备份中，请等它跑完再删除")
		return
	}
	if err := d.Store.DB.Delete(&store.MySQLBackup{}, task.ID).Error; err != nil {
		httpx.ServerError(c, "删除失败")
		return
	}
	detail := fmt.Sprintf("删除 MySQL 备份任务「%s」及其备份文件", task.Name)
	if d.Backup != nil {
		if err := d.Backup.RemoveTask(task.ID); err != nil {
			// 任务已经删了，文件清不掉只影响磁盘占用，不该让接口失败
			detail = fmt.Sprintf("删除 MySQL 备份任务「%s」，备份文件清理失败：%v", task.Name, err)
		}
	}
	middleware.SetAuditDetail(c, detail)
	httpx.OKMsg(c, "已删除", nil)
}

// runMySQLBackup 立即执行一次备份（异步，前端轮询状态）。
func (d *Deps) runMySQLBackup(c *gin.Context) {
	if d.Backup == nil {
		httpx.ServerError(c, "备份服务未启动")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "任务 ID 不合法")
		return
	}
	var task store.MySQLBackup
	if err := d.Store.DB.First(&task, uint(id)).Error; err != nil {
		httpx.NotFound(c, "备份任务不存在")
		return
	}
	if err := d.Backup.RunAsync(task.ID); err != nil {
		if err == backup.ErrRunning {
			httpx.BadRequest(c, err.Error())
			return
		}
		httpx.ServerError(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, fmt.Sprintf("手动执行 MySQL 备份「%s」：%s.%s",
		task.Name, task.Schema, scopeText(&task)))
	httpx.OKMsg(c, "已开始备份，稍候刷新查看结果", gin.H{"id": task.ID, "running": true})
}

// listMySQLBackupFiles 列出某个任务的备份文件。
func (d *Deps) listMySQLBackupFiles(c *gin.Context) {
	if d.Backup == nil {
		httpx.ServerError(c, "备份服务未启动")
		return
	}
	id, err := strconv.ParseUint(c.Query("task_id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "任务 ID 不合法")
		return
	}
	files, err := d.Backup.Files(uint(id))
	if err != nil {
		httpx.ServerError(c, "读取备份目录失败："+err.Error())
		return
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	httpx.OK(c, gin.H{"items": files, "total_size": total, "count": len(files)})
}

// downloadMySQLBackupFile 下载备份文件。
func (d *Deps) downloadMySQLBackupFile(c *gin.Context) {
	if d.Backup == nil {
		httpx.ServerError(c, "备份服务未启动")
		return
	}
	id, err := strconv.ParseUint(c.Query("task_id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "任务 ID 不合法")
		return
	}
	name := c.Query("name")
	p, err := d.Backup.FilePath(uint(id), name)
	if err != nil {
		httpx.NotFound(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "下载 MySQL 备份文件 "+name)
	// gz 也按附件给，浏览器不要自作主张解压
	c.FileAttachment(p, filepath.Base(name))
}

// deleteMySQLBackupFile 删除单个备份文件。
func (d *Deps) deleteMySQLBackupFile(c *gin.Context) {
	if d.Backup == nil {
		httpx.ServerError(c, "备份服务未启动")
		return
	}
	id, err := strconv.ParseUint(c.Query("task_id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "任务 ID 不合法")
		return
	}
	name := c.Query("name")
	if err := d.Backup.DeleteFile(uint(id), name); err != nil {
		httpx.NotFound(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "删除 MySQL 备份文件 "+name)
	httpx.OKMsg(c, "已删除", nil)
}

// ---------- Agent 侧：远程主机执行备份 ----------

// firstWriteWriter 记录是否已经开始输出。
// 一旦写出第一个字节，就没法再改 HTTP 状态码，只能中断连接。
type firstWriteWriter struct {
	w       gin.ResponseWriter
	started bool
}

func (f *firstWriteWriter) Write(p []byte) (int, error) {
	f.started = true
	return f.w.Write(p)
}

// agentMySQLBackupDump 由控制端调用：在 Agent 所在网络里连库导出，SQL 流回控制端。
// 连接参数照旧由请求头下发（控制端已解密），Agent 不落库、也不需要登录态。
func (d *Deps) agentMySQLBackupDump(c *gin.Context) {
	var opt mysqldb.DumpOptions
	if err := c.ShouldBindJSON(&opt); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}

	db, _, ok := d.mysqlDB(c, false)
	if !ok {
		return
	}
	defer db.Close()

	c.Header("Content-Type", "application/sql; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	// 行数通过 trailer 带回：响应体是纯 SQL，不能混入 JSON
	c.Header("Trailer", "X-Dump-Rows")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Hour)
	defer cancel()

	fw := &firstWriteWriter{w: c.Writer}
	stat, err := mysqldb.Dump(ctx, db, opt, fw)
	if err != nil {
		if !fw.started {
			httpx.Fail(c, http.StatusBadGateway, err.Error())
			return
		}
		// 已经输出了一半，只能断开，控制端会看到数据不完整并判定失败
		c.Abort()
		return
	}
	c.Header("X-Dump-Rows", strconv.FormatInt(stat.Rows, 10))
}
