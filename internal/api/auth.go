package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/secure"
	"warden/internal/store"
)

const tokenCookie = "warden_token"

func (d *Deps) setToken(c *gin.Context, token string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(tokenCookie, token, int(d.Cfg.TokenTTL().Seconds()), "/", "", c.Request.TLS != nil, true)
}

func (d *Deps) authStatus(c *gin.Context) {
	authed := false
	var user *store.User
	if tk, ckErr := c.Cookie(tokenCookie); ckErr == nil && tk != "" {
		if claims, err := d.Auth.Parse(tk); err == nil {
			if u, err := d.Auth.User(claims.UserID); err == nil && !u.Disabled {
				authed = true
				user = u
			}
		}
	}
	httpx.OK(c, gin.H{
		"initialized": d.Auth.Initialized(),
		"authed":      authed,
		"user":        user,
	})
}

type loginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	TOTP     string `json:"totp"`
}

func (d *Deps) bootstrap(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请填写用户名和密码")
		return
	}
	u, err := d.Auth.Bootstrap(req.Username, req.Password)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	token, _ := d.Auth.Issue(u)
	d.setToken(c, token)
	httpx.OKMsg(c, "初始化完成", gin.H{"token": token, "user": u})
}

func (d *Deps) login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请填写用户名和密码")
		return
	}
	token, u, err := d.Auth.Login(req.Username, req.Password, req.TOTP, c.ClientIP())
	if err != nil {
		httpx.Unauthorized(c, err.Error())
		return
	}
	d.setToken(c, token)
	httpx.OKMsg(c, "登录成功", gin.H{"token": token, "user": u})
}

func (d *Deps) logout(c *gin.Context) {
	c.SetCookie(tokenCookie, "", -1, "/", "", c.Request.TLS != nil, true)
	httpx.OKMsg(c, "已退出登录", nil)
}

func (d *Deps) me(c *gin.Context) {
	u, err := d.Auth.User(middleware.UID(c))
	if err != nil {
		httpx.NotFound(c, "用户不存在")
		return
	}
	httpx.OK(c, u)
}

type pwdReq struct {
	Old string `json:"old_password"`
	New string `json:"new_password" binding:"required"`
}

func (d *Deps) changePassword(c *gin.Context) {
	var req pwdReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	if err := d.Auth.ChangePassword(middleware.UID(c), req.Old, req.New); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "修改登录密码")
	httpx.OKMsg(c, "密码已更新", nil)
}

func (d *Deps) totpSetup(c *gin.Context) {
	secret := secure.NewTOTPSecret()
	u := middleware.Username(c)
	httpx.OK(c, gin.H{
		"secret": secret,
		"url":    secure.TOTPProvisionURL("Warden", u, secret),
	})
}

type totpEnableReq struct {
	Secret string `json:"secret" binding:"required"`
	Code   string `json:"code" binding:"required"`
}

func (d *Deps) totpEnable(c *gin.Context) {
	var req totpEnableReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	if !secure.VerifyTOTP(req.Secret, req.Code) {
		httpx.BadRequest(c, "验证码不正确，请检查手机时间是否准确")
		return
	}
	if err := d.Auth.EnableTOTP(middleware.UID(c), req.Secret); err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OKMsg(c, "两步验证已开启", nil)
}

func (d *Deps) totpDisable(c *gin.Context) {
	if err := d.Auth.DisableTOTP(middleware.UID(c)); err != nil {
		httpx.ServerError(c, err.Error())
		return
	}
	httpx.OKMsg(c, "两步验证已关闭", nil)
}

// 供其他 handler 复用的时间格式化
func nowStr() string { return time.Now().Format(time.RFC3339) }
