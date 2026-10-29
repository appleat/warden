package api

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"warden/internal/fleet"
	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/store"
)

func (d *Deps) listHosts(c *gin.Context) {
	views, err := d.Fleet.List()
	if err != nil {
		httpx.ServerError(c, "读取主机列表失败")
		return
	}
	// 本机状态直接取本地实时指标，避免显示为空
	if snap, _ := d.Metrics.Snapshot(); true {
		for i := range views {
			if views[i].Kind != "local" {
				continue
			}
			views[i].Hostname = snap.Host.Hostname
			views[i].Platform = snap.Host.Platform
			views[i].Kernel = snap.Host.Kernel
			views[i].Arch = snap.Host.Arch
			views[i].CPUCores = snap.Host.CPUCores
			views[i].TotalMem = snap.Host.TotalMem
			views[i].Uptime = snap.Host.Uptime
			views[i].Version = d.Version
			views[i].FileRoot = d.Files.RootDisplay()
			if d.Docker != nil {
				views[i].DockerOK = d.Docker.Status(c.Request.Context()).Available
			}
			if d.FW != nil {
				views[i].Firewall = d.FW.Status(c.Request.Context()).Driver
			}
		}
	}
	httpx.OK(c, gin.H{"items": views})
}

type hostReq struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	TLS       bool   `json:"tls"`
	Insecure  bool   `json:"insecure"`
	GroupName string `json:"group_name"`
	Remark    string `json:"remark"`
	Enabled   *bool  `json:"enabled"`
}

// normalizeAddr 接受 "1.2.3.4:8090"、"http://1.2.3.4:8090" 等写法，统一去掉协议与尾部斜杠。
func normalizeAddr(raw string) string {
	a := strings.TrimSpace(raw)
	a = strings.TrimPrefix(a, "http://")
	a = strings.TrimPrefix(a, "https://")
	return strings.TrimRight(a, "/")
}

func (d *Deps) createHost(c *gin.Context) {
	var req hostReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	addr := normalizeAddr(req.Address)
	if req.Name == "" {
		httpx.BadRequest(c, "请填写主机名称")
		return
	}
	if addr == "" {
		httpx.BadRequest(c, "请填写 Agent 地址，例如 10.0.0.12:8090")
		return
	}

	var dup int64
	d.Store.DB.Model(&store.Host{}).Where("address = ?", addr).Count(&dup)
	if dup > 0 {
		httpx.BadRequest(c, "该地址已存在，请勿重复添加")
		return
	}

	token := fleet.NewToken()
	enc, err := d.Fleet.EncryptToken(token)
	if err != nil {
		httpx.ServerError(c, "生成主机令牌失败")
		return
	}

	h := store.Host{
		Name:      req.Name,
		Kind:      "agent",
		Address:   addr,
		TLS:       req.TLS,
		Insecure:  req.Insecure,
		Token:     enc,
		GroupName: strings.TrimSpace(req.GroupName),
		Remark:    req.Remark,
		Enabled:   req.Enabled == nil || *req.Enabled,
	}
	if err := d.Store.DB.Create(&h).Error; err != nil {
		httpx.ServerError(c, "保存主机失败")
		return
	}
	d.Fleet.Invalidate(h.ID)
	middleware.SetAuditDetail(c, "添加主机 "+h.Name+" ("+h.Address+")")

	// 令牌只在创建时明文返回一次
	httpx.OKMsg(c, "主机已添加，请把令牌配置到目标机 Agent", gin.H{
		"host":        h,
		"token":       token,
		"install_cmd": fleet.InstallCommand(addr, token, h.Name),
		"config_snippet": fmt.Sprintf(
			"mode: agent\nagent_listen: \":8090\"\nagent_token: \"%s\"\nagent_name: \"%s\"\nfile_root: \"/\"",
			token, h.Name),
	})
}

func (d *Deps) updateHost(c *gin.Context) {
	h, err := d.Fleet.Get(parseID(c))
	if err != nil {
		httpx.NotFound(c, err.Error())
		return
	}

	var req hostReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}

	if n := strings.TrimSpace(req.Name); n != "" {
		h.Name = n
	}
	h.GroupName = strings.TrimSpace(req.GroupName)
	h.Remark = req.Remark
	if req.Enabled != nil {
		h.Enabled = *req.Enabled
	}

	// 本机不允许改地址与协议
	if h.Kind != "local" {
		if addr := normalizeAddr(req.Address); addr != "" && addr != h.Address {
			h.Address = addr
			d.Fleet.Invalidate(h.ID)
		}
		h.TLS = req.TLS
		h.Insecure = req.Insecure
	}

	if err := d.Store.DB.Save(h).Error; err != nil {
		httpx.ServerError(c, "保存主机失败")
		return
	}
	_ = d.Fleet.Reload()
	middleware.SetAuditDetail(c, "修改主机 "+h.Name)

	if h.Kind == "agent" {
		d.Fleet.Probe(c.Request.Context(), h)
	}
	httpx.OKMsg(c, "已保存", h)
}

func (d *Deps) deleteHost(c *gin.Context) {
	h, err := d.Fleet.Get(parseID(c))
	if err != nil {
		httpx.NotFound(c, err.Error())
		return
	}
	if h.Kind == "local" {
		httpx.BadRequest(c, "本机是控制端自身，不能删除")
		return
	}
	if err := d.Store.DB.Delete(&store.Host{}, h.ID).Error; err != nil {
		httpx.ServerError(c, "删除失败")
		return
	}
	d.Fleet.Invalidate(h.ID)
	middleware.SetAuditDetail(c, "删除主机 "+h.Name)
	httpx.OKMsg(c, "已删除", nil)
}

func (d *Deps) testHost(c *gin.Context) {
	h, err := d.Fleet.Get(parseID(c))
	if err != nil {
		httpx.NotFound(c, err.Error())
		return
	}
	if h.Kind == "local" {
		httpx.OK(c, gin.H{"online": true, "message": "本机无需探测"})
		return
	}
	st := d.Fleet.Probe(c.Request.Context(), h)
	msg := "连接正常"
	if !st.Online {
		msg = st.Error
	}
	httpx.OK(c, gin.H{"online": st.Online, "message": msg, "status": st})
}

// rotateHostToken 重置令牌，旧令牌立即失效。
func (d *Deps) rotateHostToken(c *gin.Context) {
	h, err := d.Fleet.Get(parseID(c))
	if err != nil {
		httpx.NotFound(c, err.Error())
		return
	}
	if h.Kind == "local" {
		httpx.BadRequest(c, "本机没有访问令牌")
		return
	}
	token := fleet.NewToken()
	enc, err := d.Fleet.EncryptToken(token)
	if err != nil {
		httpx.ServerError(c, "生成令牌失败")
		return
	}
	h.Token = enc
	if err := d.Store.DB.Save(h).Error; err != nil {
		httpx.ServerError(c, "保存失败")
		return
	}
	d.Fleet.Invalidate(h.ID)
	middleware.SetAuditDetail(c, "重置主机令牌 "+h.Name)
	httpx.OKMsg(c, "令牌已重置，请更新目标机 Agent 配置", gin.H{
		"token":       token,
		"install_cmd": fleet.InstallCommand(h.Address, token, h.Name),
	})
}

// parseID 读取路径参数 :id，失败返回 0。
func parseID(c *gin.Context) uint {
	n, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		return 0
	}
	return uint(n)
}
