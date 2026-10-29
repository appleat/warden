package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/system"
)

// processList 返回当前主机的进程列表（默认按 CPU 降序取前 100）。
// 该接口挂在主机组下，本机由控制端直接采样，远程由 Agent 采样。
func (d *Deps) processList(c *gin.Context) {
	sortBy := c.DefaultQuery("sort", "cpu")
	order := c.DefaultQuery("order", "desc")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	desc := order != "asc"
	orderOut := "desc"
	if !desc {
		orderOut = "asc"
	}
	items, total, err := system.ListProcesses(sortBy, desc, c.Query("q"), limit)
	if err != nil {
		httpx.ServerError(c, "读取进程列表失败："+err.Error())
		return
	}
	httpx.OK(c, gin.H{
		"items":    items,
		"total":    total,
		"sort":     sortBy,
		"order":    orderOut,
		"limit":    limit,
		"interval": system.ProcSampleInterval.Milliseconds(), // CPU 采样窗口，前端用于说明数据口径
	})
}

// processKill 结束指定进程。force=true 走 SIGKILL。
func (d *Deps) processKill(c *gin.Context) {
	var req struct {
		PID   int32 `json:"pid"`
		Force bool  `json:"force"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PID <= 0 {
		httpx.BadRequest(c, "缺少有效的进程 PID")
		return
	}
	if err := system.KillProcess(req.PID, req.Force); err != nil {
		middleware.SetAuditDetail(c, "结束进程失败 pid="+strconv.Itoa(int(req.PID)))
		httpx.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	mode := "SIGTERM"
	if req.Force {
		mode = "SIGKILL"
	}
	middleware.SetAuditDetail(c, "结束进程 pid="+strconv.Itoa(int(req.PID))+" ("+mode+")")
	httpx.OKMsg(c, "已发送结束信号（"+mode+"）", gin.H{"pid": req.PID})
}
