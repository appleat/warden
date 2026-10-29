package api

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/store"
)

type uptimeRow struct {
	MonitorID uint
	Total     int64
	Oks       int64
}

func (d *Deps) uptimeMap() map[uint]float64 {
	since := time.Now().Add(-24 * time.Hour)
	var rows []uptimeRow
	d.Store.DB.Raw(`SELECT monitor_id, COUNT(*) as total,
		SUM(CASE WHEN ok = 1 THEN 1 ELSE 0 END) as oks
		FROM monitor_results WHERE created_at >= ? GROUP BY monitor_id`, since).Scan(&rows)
	out := make(map[uint]float64, len(rows))
	for _, r := range rows {
		if r.Total > 0 {
			out[r.MonitorID] = float64(r.Oks) / float64(r.Total) * 100
		}
	}
	return out
}

func (d *Deps) listMonitors(c *gin.Context) {
	var list []store.Monitor
	if err := d.Store.DB.Order("id asc").Find(&list).Error; err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	up := d.uptimeMap()
	items := make([]store.Monitor, 0, len(list))
	for i := range list {
		items = append(items, d.Monitor.View(&list[i], up[list[i].ID]))
	}
	httpx.OK(c, gin.H{"items": items})
}

func (d *Deps) monitorSummary(c *gin.Context) {
	var list []store.Monitor
	d.Store.DB.Where("enabled = ?", true).Find(&list)
	up, down, pending := 0, 0, 0
	downItems := []gin.H{}
	for i := range list {
		v := d.Monitor.View(&list[i], 0)
		switch v.Status {
		case "up":
			up++
		case "down":
			down++
			downItems = append(downItems, gin.H{"id": v.ID, "name": v.Name, "target": v.Target, "message": v.LastMessage})
		default:
			pending++
		}
	}
	httpx.OK(c, gin.H{"total": len(list), "up": up, "down": down, "pending": pending, "down_items": downItems})
}

func (d *Deps) saveMonitor(c *gin.Context) {
	var m store.Monitor
	if err := c.ShouldBindJSON(&m); err != nil {
		httpx.BadRequest(c, "参数解析失败: "+err.Error())
		return
	}
	if m.Name == "" || m.Target == "" {
		httpx.BadRequest(c, "名称与检测目标不能为空")
		return
	}
	if m.Type == "" {
		m.Type = "http"
	}
	if m.Interval < 10 {
		m.Interval = 60
	}
	if m.Timeout <= 0 {
		m.Timeout = 10
	}
	if m.FailRetries <= 0 {
		m.FailRetries = 2
	}
	if m.ExpectCode <= 0 {
		m.ExpectCode = 200
	}

	if idStr := c.Param("id"); idStr != "" {
		id, _ := strconv.Atoi(idStr)
		var old store.Monitor
		if err := d.Store.DB.First(&old, id).Error; err != nil {
			httpx.NotFound(c, "监控项不存在")
			return
		}
		m.ID = old.ID
		m.CreatedAt = old.CreatedAt
	}
	if err := d.Store.DB.Save(&m).Error; err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "保存监控项 "+m.Name)
	httpx.OKMsg(c, "已保存", m)
}

func (d *Deps) deleteMonitor(c *gin.Context) {
	id := c.Param("id")
	if err := d.Store.DB.Delete(&store.Monitor{}, id).Error; err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	d.Store.DB.Where("monitor_id = ?", id).Delete(&store.MonitorResult{})
	httpx.OKMsg(c, "已删除", nil)
}

// toggleMonitor 启动 / 停止一个监控任务（只改调度开关，不删除数据）。
func (d *Deps) toggleMonitor(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	var m store.Monitor
	if err := d.Store.DB.First(&m, id).Error; err != nil {
		httpx.NotFound(c, "监控项不存在")
		return
	}
	if err := d.Store.DB.Model(&m).Update("enabled", req.Enabled).Error; err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	m.Enabled = req.Enabled
	d.Monitor.SetEnabled(m.ID, req.Enabled)
	if req.Enabled {
		middleware.SetAuditDetail(c, "启动监控 "+m.Name)
	} else {
		middleware.SetAuditDetail(c, "停止监控 "+m.Name)
	}
	httpx.OK(c, d.Monitor.View(&m, 0))
}

func (d *Deps) checkMonitor(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := d.Monitor.CheckNow(uint(id)); err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	var m store.Monitor
	d.Store.DB.First(&m, id)
	httpx.OK(c, d.Monitor.ViewLive(&m, 0))
}

func (d *Deps) monitorHistory(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	points, uptime := d.Monitor.History(uint(id), hours)
	httpx.OK(c, gin.H{"points": points, "uptime": uptime, "hours": hours})
}

func (d *Deps) getAlertSetting(c *gin.Context) {
	url, enabled := d.Notify.Config()
	httpx.OK(c, gin.H{"webhook": url, "enabled": enabled})
}

func (d *Deps) saveAlertSetting(c *gin.Context) {
	var req struct {
		Webhook string `json:"webhook"`
		Enabled bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	d.Notify.Configure(req.Webhook, req.Enabled)
	middleware.SetAuditDetail(c, "更新告警通知配置")
	httpx.OKMsg(c, "已保存", nil)
}

func (d *Deps) listAudit(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	q := d.Store.DB.Model(&store.AuditLog{})
	if kw := c.Query("q"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("username LIKE ? OR action LIKE ? OR target LIKE ?", like, like, like)
	}

	var total int64
	q.Count(&total)
	var items []store.AuditLog
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&items)
	httpx.OK(c, httpx.Page{Items: items, Total: total, Page: page, Size: size})
}
