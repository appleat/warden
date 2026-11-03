package api

import (
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/store"
)

// favScope 把请求里的 ?id=（SSH 主机，SFTP）或 ?host=（本机/纳管主机）归一成
// 收藏夹的作用域键。两类主机的自增 ID 各自独立，所以必须带前缀区分，
// 否则「SSH 主机 1」和「纳管主机 1」的收藏会串在一起。
func favScope(c *gin.Context) (string, bool) {
	if id := c.Query("id"); id != "" {
		n, err := strconv.Atoi(id)
		if err != nil || n <= 0 {
			return "", false
		}
		return "ssh:" + strconv.Itoa(n), true
	}
	if h := c.Query("host"); h != "" {
		n, err := strconv.Atoi(h)
		if err != nil || n <= 0 {
			return "", false
		}
		return "host:" + strconv.Itoa(n), true
	}
	return "host:1", true
}

// normFavPath 统一分隔符并去掉末尾斜杠（根路径保留），
// 让 "/data/logs" 与 "/data/logs/" 视为同一条收藏。
func normFavPath(p string) string {
	s := strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if s == "" {
		return "/"
	}
	if len(s) > 1 {
		s = strings.TrimRight(s, "/")
	}
	if s == "" {
		return "/"
	}
	// Windows 盘符根 "D:" 补成 "D:/"
	if len(s) == 2 && s[1] == ':' {
		s += "/"
	}
	return s
}

func favName(p, given string) string {
	if given = strings.TrimSpace(given); given != "" {
		return given
	}
	base := path.Base(p)
	if base == "." || base == "/" || base == "" {
		return p
	}
	return base
}

func (d *Deps) listFileFavorites(c *gin.Context) {
	scope, ok := favScope(c)
	if !ok {
		httpx.BadRequest(c, "主机参数不正确")
		return
	}
	out := make([]store.FileFavorite, 0, 8)
	if err := d.Store.DB.
		Where("user_id = ? AND scope = ?", middleware.UID(c), scope).
		Order("created_at ASC").Find(&out).Error; err != nil {
		httpx.ServerError(c, "读取收藏夹失败")
		return
	}
	httpx.OK(c, out)
}

func (d *Deps) addFileFavorite(c *gin.Context) {
	scope, ok := favScope(c)
	if !ok {
		httpx.BadRequest(c, "主机参数不正确")
		return
	}
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	p := normFavPath(req.Path)
	if p == "" {
		httpx.BadRequest(c, "请提供目录路径")
		return
	}
	uid := middleware.UID(c)

	// 已收藏过就只更新显示名，避免同一目录出现两条（Windows 路径大小写不敏感）
	var exist store.FileFavorite
	err := d.Store.DB.
		Where("user_id = ? AND scope = ? AND LOWER(path) = ?", uid, scope, strings.ToLower(p)).
		First(&exist).Error
	if err == nil {
		if name := favName(p, req.Name); name != exist.Name {
			exist.Name = name
			d.Store.DB.Model(&exist).Update("name", name)
		}
		httpx.OKMsg(c, "已在收藏夹中", exist)
		return
	}

	fav := store.FileFavorite{UserID: uid, Scope: scope, Path: p, Name: favName(p, req.Name)}
	if err := d.Store.DB.Create(&fav).Error; err != nil {
		httpx.ServerError(c, "收藏失败")
		return
	}
	httpx.OKMsg(c, "已收藏", fav)
}

func (d *Deps) deleteFileFavorite(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		httpx.BadRequest(c, "参数不正确")
		return
	}
	res := d.Store.DB.
		Where("id = ? AND user_id = ?", id, middleware.UID(c)).
		Delete(&store.FileFavorite{})
	if res.Error != nil {
		httpx.ServerError(c, "取消收藏失败")
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "收藏不存在")
		return
	}
	httpx.OKMsg(c, "已取消收藏", nil)
}
