package api

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/store"
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
	httpx.OK(c, gin.H{"items": d.Metrics.TopProcesses(n)})
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
