package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/secure"
	"warden/internal/store"
	"warden/internal/system"
)

func (d *Deps) firewallStatus(c *gin.Context) {
	st := d.FW.Status(c.Request.Context())
	httpx.OK(c, st)
}

// riskyActions 判断本次改动是否可能导致自身被锁在门外。
func riskyRule(r system.Rule) bool {
	return r.Action == "deny"
}

func (d *Deps) addFirewallRule(c *gin.Context) {
	var r system.Rule
	if err := c.ShouldBindJSON(&r); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	snap, snapErr := d.FW.Snapshot(c.Request.Context())
	if err := d.FW.AddRule(c.Request.Context(), r); err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	resp := gin.H{"risk": false}
	if riskyRule(r) && snapErr == nil {
		token := d.FW.ScheduleRevert(30, snap)
		resp["risk"] = true
		resp["revert_token"] = token
		resp["revert_seconds"] = 30
	}
	middleware.SetAuditDetail(c, "防火墙添加规则 "+r.Action+" "+r.Port+"/"+r.Proto)
	httpx.OKMsg(c, "规则已生效", resp)
}

func (d *Deps) deleteFirewallRule(c *gin.Context) {
	snap, snapErr := d.FW.Snapshot(c.Request.Context())
	if err := d.FW.DeleteRule(c.Request.Context(), c.Param("id")); err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	resp := gin.H{"risk": false}
	if snapErr == nil {
		token := d.FW.ScheduleRevert(30, snap)
		resp["risk"] = true
		resp["revert_token"] = token
		resp["revert_seconds"] = 30
	}
	middleware.SetAuditDetail(c, "防火墙删除规则 #"+c.Param("id"))
	httpx.OKMsg(c, "规则已删除", resp)
}

func (d *Deps) toggleFirewall(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	snap, snapErr := d.FW.Snapshot(c.Request.Context())
	if err := d.FW.SetEnabled(c.Request.Context(), req.Enabled); err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	resp := gin.H{"risk": false}
	if snapErr == nil {
		token := d.FW.ScheduleRevert(30, snap)
		resp["risk"] = true
		resp["revert_token"] = token
		resp["revert_seconds"] = 30
	}
	middleware.SetAuditDetail(c, "防火墙开关 enabled="+strconv.FormatBool(req.Enabled))
	httpx.OKMsg(c, "防火墙状态已切换", resp)
}

func (d *Deps) confirmFirewall(c *gin.Context) {
	var req struct {
		Token string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "缺少确认令牌")
		return
	}
	if !d.FW.ConfirmRevert(req.Token) {
		httpx.BadRequest(c, "确认已失效（可能已自动回滚）")
		return
	}
	httpx.OKMsg(c, "已确认保留本次改动", nil)
}

// ---------- SSH 主机管理 ----------

func (d *Deps) listSSHHosts(c *gin.Context) {
	var hosts []store.SSHHost
	if err := d.Store.DB.Order("id asc").Find(&hosts).Error; err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": hosts})
}

type sshHostReq struct {
	ID         uint   `json:"id"`
	Name       string `json:"name" binding:"required"`
	Host       string `json:"host" binding:"required"`
	Port       int    `json:"port"`
	Username   string `json:"username" binding:"required"`
	AuthType   string `json:"auth_type"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase"`
	Remark     string `json:"remark"`
}

func (d *Deps) saveSSHHost(c *gin.Context) {
	var req sshHostReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请填写主机、用户名和名称")
		return
	}
	if req.Port <= 0 {
		req.Port = 22
	}
	if req.AuthType == "" {
		req.AuthType = "password"
	}

	encSecret, err := secure.Encrypt(d.Cfg.SecretKey(), req.Secret)
	if err != nil {
		httpx.ServerError(c, "凭据加密失败")
		return
	}
	encPass, err := secure.Encrypt(d.Cfg.SecretKey(), req.Passphrase)
	if err != nil {
		httpx.ServerError(c, "凭据加密失败")
		return
	}

	h := store.SSHHost{
		Name: req.Name, Host: req.Host, Port: req.Port, Username: req.Username,
		AuthType: req.AuthType, Remark: req.Remark,
	}
	if req.ID > 0 {
		if err := d.Store.DB.First(&h, req.ID).Error; err != nil {
			httpx.NotFound(c, "主机不存在")
			return
		}
		h.Name, h.Host, h.Port, h.Username = req.Name, req.Host, req.Port, req.Username
		h.AuthType, h.Remark = req.AuthType, req.Remark
		if req.Secret != "" {
			h.Secret = encSecret
		}
		if req.Passphrase != "" {
			h.Passphrase = encPass
		}
	} else {
		if req.Secret == "" {
			httpx.BadRequest(c, "请填写密码或私钥")
			return
		}
		h.Secret, h.Passphrase = encSecret, encPass
	}
	if err := d.Store.DB.Save(&h).Error; err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OKMsg(c, "已保存", h)
}

func (d *Deps) targetFromHost(h *store.SSHHost) (system.SSHTarget, error) {
	secret, err := secure.Decrypt(d.Cfg.SecretKey(), h.Secret)
	if err != nil {
		return system.SSHTarget{}, err
	}
	pass, err := secure.Decrypt(d.Cfg.SecretKey(), h.Passphrase)
	if err != nil {
		return system.SSHTarget{}, err
	}
	return system.SSHTarget{
		Host: h.Host, Port: h.Port, User: h.Username,
		AuthType: h.AuthType, Secret: secret, Passphrase: pass,
	}, nil
}

func (d *Deps) testSSHHost(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var h store.SSHHost
	if err := d.Store.DB.First(&h, id).Error; err != nil {
		httpx.NotFound(c, "主机不存在")
		return
	}
	t, err := d.targetFromHost(&h)
	if err != nil {
		httpx.ServerError(c, "凭据解密失败")
		return
	}
	if err := system.TestSSH(t); err != nil {
		httpx.Fail(c, http.StatusBadGateway, "连接失败: "+err.Error())
		return
	}
	out, _ := system.RunSSHCommand(t, "uname -a; uptime", 8e9)
	httpx.OKMsg(c, "连接成功", gin.H{"output": out})
}

func (d *Deps) deleteSSHHost(c *gin.Context) {
	if err := d.Store.DB.Delete(&store.SSHHost{}, c.Param("id")).Error; err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OKMsg(c, "已删除", nil)
}
