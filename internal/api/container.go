package api

import (
	"bufio"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/platform/docker"
)

func (d *Deps) listContainers(c *gin.Context) {
	all := c.DefaultQuery("all", "true") != "false"
	items, err := d.Docker.List(c.Request.Context(), all, c.Query("q"))
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	// 资源占用要额外调用 Docker stats 接口，仅在前端显式要求时采集
	if c.Query("stats") == "true" {
		d.Docker.FillStats(c.Request.Context(), items)
	}
	httpx.OK(c, gin.H{"items": items})
}

func (d *Deps) inspectContainer(c *gin.Context) {
	info, err := d.Docker.Inspect(c.Request.Context(), c.Param("id"))
	if err != nil {
		httpx.Fail(c, http.StatusNotFound, err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", []byte(`{"code":0,"msg":"ok","data":`+string(info)+`}`))
}

func (d *Deps) containerAction(c *gin.Context) {
	id := c.Param("id")
	action := c.Param("action")
	ctx := c.Request.Context()
	var err error
	switch action {
	case "start":
		err = d.Docker.Start(ctx, id)
	case "stop":
		err = d.Docker.Stop(ctx, id)
	case "restart":
		err = d.Docker.Restart(ctx, id)
	case "pause":
		err = d.Docker.Pause(ctx, id)
	case "unpause":
		err = d.Docker.Unpause(ctx, id)
	default:
		httpx.BadRequest(c, "不支持的操作: "+action)
		return
	}
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "容器 "+id+" 执行 "+action)
	httpx.OKMsg(c, "操作成功", nil)
}

func (d *Deps) removeContainer(c *gin.Context) {
	force := c.Query("force") == "true"
	if err := d.Docker.Remove(c.Request.Context(), c.Param("id"), force); err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "删除容器 "+c.Param("id")+" force="+strconv.FormatBool(force))
	httpx.OKMsg(c, "容器已删除", nil)
}

func (d *Deps) createContainer(c *gin.Context) {
	var spec docker.CreateSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		httpx.BadRequest(c, "参数解析失败: "+err.Error())
		return
	}
	id, err := d.Docker.Create(c.Request.Context(), spec)
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "创建容器 "+spec.Name+" 镜像 "+spec.Image)
	httpx.OKMsg(c, "容器已创建并启动", gin.H{"id": id})
}

func (d *Deps) containerStats(c *gin.Context) {
	st, err := d.Docker.StatOnce(c.Request.Context(), c.Param("id"))
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OK(c, st)
}

// containerStatsBatch 批量采集资源占用，供列表页在容器渲染完成后再异步补齐 CPU / 内存。
// 带上 ids 就只算这些容器，否则算当前全部运行中的容器。
func (d *Deps) containerStatsBatch(c *gin.Context) {
	var ids []string
	if raw := strings.TrimSpace(c.Query("ids")); raw != "" {
		ids = strings.Split(raw, ",")
	}
	stats, err := d.Docker.StatsFor(c.Request.Context(), ids)
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": stats})
}

func (d *Deps) containerLogs(c *gin.Context) {
	tail := c.DefaultQuery("tail", "200")
	rc, tty, err := d.Docker.Logs(c.Request.Context(), c.Param("id"), docker.LogOptions{
		Tail: tail, Follow: false, Timestamps: c.Query("timestamps") == "true",
	})
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	defer rc.Close()

	out, err := readDockerStream(rc, tty)
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"logs": out})
}

// readDockerStream 解复用 Docker 日志流（非 TTY 带 8 字节头）。
func readDockerStream(rc io.Reader, tty bool) (string, error) {
	var sb strings.Builder
	if tty {
		_, err := io.Copy(&sb, rc)
		return sb.String(), err
	}
	_, err := stdcopy.StdCopy(&sb, &sb, rc)
	return sb.String(), err
}

// ---------- docker-compose 堆栈 ----------

func (d *Deps) listComposeStacks(c *gin.Context) {
	items, err := d.Docker.Stacks(c.Request.Context())
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

func (d *Deps) getComposeFile(c *gin.Context) {
	data, err := d.Docker.ComposeFile(c.Request.Context(), c.Query("project"))
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OK(c, data)
}

func (d *Deps) saveComposeFile(c *gin.Context) {
	var req struct {
		Project string `json:"project" binding:"required"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	path, err := d.Docker.SaveComposeFile(c.Request.Context(), req.Project, req.Content)
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "保存 compose 文件 "+path)
	httpx.OKMsg(c, "已保存，原文件备份为 .bak", gin.H{"path": path})
}

// composeUp 应用堆栈配置。无论成败都返回 200 + {ok, output}，
// 让前端把 compose 的完整输出展示在日志框里，而不是塞进一条报错气泡。
func (d *Deps) composeUp(c *gin.Context) {
	var req struct {
		Project string `json:"project" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	out, err := d.Docker.ComposeUp(c.Request.Context(), req.Project)
	if err != nil {
		httpx.OK(c, gin.H{"ok": false, "output": out})
		return
	}
	middleware.SetAuditDetail(c, "应用 compose 项目 "+req.Project)
	httpx.OKMsg(c, "已应用", gin.H{"ok": true, "output": out})
}

// ---------- 镜像 / 卷 / 网络 ----------

func (d *Deps) listImages(c *gin.Context) {
	items, err := d.Docker.Images(c.Request.Context())
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

func (d *Deps) pullImage(c *gin.Context) {
	var req struct {
		Image string `json:"image" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请填写镜像名称")
		return
	}
	rc, err := d.Docker.PullStream(c.Request.Context(), req.Image)
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	defer rc.Close()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if p, ok := docker.ParseProgress(line); ok {
			if p.Error != "" {
				c.SSEvent("error", gin.H{"message": p.Error})
				c.Writer.Flush()
				return
			}
			c.SSEvent("progress", p)
			c.Writer.Flush()
			continue
		}
		c.SSEvent("progress", gin.H{"status": string(line)})
		c.Writer.Flush()
	}
	c.SSEvent("done", gin.H{"ok": true})
	c.Writer.Flush()
	middleware.SetAuditDetail(c, "拉取镜像 "+req.Image)
}

func (d *Deps) removeImage(c *gin.Context) {
	force := c.Query("force") == "true"
	if err := d.Docker.RemoveImage(c.Request.Context(), c.Param("id"), force); err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OKMsg(c, "镜像已删除", nil)
}

func (d *Deps) listVolumes(c *gin.Context) {
	items, err := d.Docker.Volumes(c.Request.Context())
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

func (d *Deps) listNetworks(c *gin.Context) {
	items, err := d.Docker.Networks(c.Request.Context())
	if err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items})
}
