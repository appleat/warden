package api

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/store"
	"warden/internal/system"
)

func (d *Deps) overview(c *gin.Context) {
	snap, hist := d.Metrics.Snapshot()
	dockerSt := d.Docker.Status(c.Request.Context())

	// Agent 模式下没有数据库与监控调度器，返回空统计即可
	mon := gin.H{"total": 0, "up": 0, "down": 0, "pending": 0}
	if d.Store != nil && d.Store.DB != nil && d.Monitor != nil {
		var monitors []store.Monitor
		d.Store.DB.Find(&monitors)
		up, down, pending := 0, 0, 0
		for i := range monitors {
			v := d.Monitor.View(&monitors[i], 0)
			switch v.Status {
			case "up":
				up++
			case "down":
				down++
			default:
				pending++
			}
		}
		mon = gin.H{"total": len(monitors), "up": up, "down": down, "pending": pending}
	}

	httpx.OK(c, gin.H{
		"metrics": snap,
		"history": hist,
		"docker":  dockerSt,
		"monitor": mon,
		"version": d.Version,
	})
}

func (d *Deps) processes(c *gin.Context) {
	n, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if n <= 0 || n > 50 {
		n = 10
	}
	// 这里复用系统进程模块的采样逻辑：metrics.TopProcesses 用的
	// gopsutil Process.CPUPercent 在 Linux 是「自启动以来的平均值」，
	// 在 Windows 上更是直接给出 90% 这种离谱数字，而且要对全部进程取用户名和命令行，
	// 每 15 秒轮询一次就烧掉一大截 CPU（实测单次约 170ms）。
	// 换成同一套采样后，看板与「系统管理 → 进程管理」里的 CPU 数字也一致了。
	items, _, err := system.ListProcesses("cpu", true, "", n)
	if err != nil {
		httpx.ServerError(c, "读取进程列表失败："+err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

func (d *Deps) dockerStatus(c *gin.Context) {
	httpx.OK(c, d.Docker.Status(c.Request.Context()))
}

func (d *Deps) systemInfo(c *gin.Context) {
	snap, _ := d.Metrics.Snapshot()
	httpx.OK(c, gin.H{
		"host":       snap.Host,
		"file_root":  d.Files.RootDisplay(),
		"version":    d.Version,
		"uptime":     snap.Host.Uptime,
		"go_version": goVersion(),
	})
}
