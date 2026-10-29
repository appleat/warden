package api

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"warden/internal/filemgr"
	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/store"
)

// fileBackend 根据请求参数选择文件操作后端：带 ?id= 时走对应 SSH 主机的
// SFTP；否则走本机磁盘。调用方负责 defer b.Close()。
func (d *Deps) fileBackend(c *gin.Context) (filemgr.Backend, error) {
	if idStr := c.Query("id"); idStr != "" {
		id, err := strconv.Atoi(idStr)
		if err != nil || id <= 0 {
			return nil, errors.New("无效的主机 id")
		}
		var h store.SSHHost
		if err := d.Store.DB.First(&h, id).Error; err != nil {
			return nil, errors.New("主机不存在")
		}
		t, err := d.targetFromHost(&h)
		if err != nil {
			return nil, errors.New("凭据解密失败，请重新编辑该主机并保存密码")
		}
		return filemgr.NewSFTP(t)
	}
	return d.Files, nil
}

func (d *Deps) listFiles(c *gin.Context) {
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	p := c.Query("path")
	cur, items, err := b.List(p)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	root := b.RootDisplay()
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(cur)))
	if cur == root {
		// 已在根目录，不能再往上，避免退到根目录之外
		parent = cur
	}
	httpx.OK(c, gin.H{
		"path":   cur,
		"parent": parent,
		"root":   root,
		"items":  items,
	})
}

func (d *Deps) downloadFile(c *gin.Context) {
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	p := c.Query("path")
	info, err := b.Stat(p)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	if info.IsDir() {
		httpx.BadRequest(c, "暂不支持直接下载目录，请先压缩")
		return
	}
	f, err := b.Open(p)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer f.Close()
	name := path.Base(p)
	c.Header("Content-Disposition", "attachment; filename=\""+name+"\"")
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(info.Size(), 10))
	io.Copy(c.Writer, f)
}

func (d *Deps) readFile(c *gin.Context) {
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	content, size, err := b.Read(c.Query("path"))
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"content": content, "size": size})
}

func (d *Deps) writeFile(c *gin.Context) {
	var req struct {
		Path    string `json:"path" binding:"required"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	if err := b.Write(req.Path, req.Content); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "编辑文件 "+req.Path)
	httpx.OKMsg(c, "已保存", nil)
}

func (d *Deps) mkdir(c *gin.Context) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name" binding:"required"`
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请填写目录名称")
		return
	}
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	perm := parseMode(req.Mode, 0o755)
	p, err := b.Mkdir(req.Path, req.Name, perm)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.OKMsg(c, "目录已创建", gin.H{"path": p})
}

func (d *Deps) touch(c *gin.Context) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请填写文件名称")
		return
	}
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	p, err := b.CreateFile(req.Path, req.Name)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.OKMsg(c, "文件已创建", gin.H{"path": p})
}

func (d *Deps) rename(c *gin.Context) {
	var req struct {
		From string `json:"from" binding:"required"`
		To   string `json:"to" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	if err := b.Rename(req.From, req.To); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "重命名 "+req.From+" -> "+req.To)
	httpx.OKMsg(c, "已重命名", nil)
}

func (d *Deps) copyPath(c *gin.Context) {
	var req struct {
		From string `json:"from" binding:"required"`
		To   string `json:"to" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	if err := b.Copy(req.From, req.To); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.OKMsg(c, "已复制", nil)
}

func (d *Deps) chmod(c *gin.Context) {
	var req struct {
		Path string `json:"path" binding:"required"`
		Mode string `json:"mode" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	if err := b.Chmod(req.Path, parseMode(req.Mode, 0o644)); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "修改权限 "+req.Path+" "+req.Mode)
	httpx.OKMsg(c, "权限已修改", nil)
}

func (d *Deps) compress(c *gin.Context) {
	var req struct {
		Path   string `json:"path" binding:"required"`
		Format string `json:"format"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	out, err := b.Compress(req.Path, req.Format)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.OKMsg(c, "压缩完成", gin.H{"path": out})
}

func (d *Deps) extract(c *gin.Context) {
	var req struct {
		Path string `json:"path" binding:"required"`
		Dest string `json:"dest"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数不完整")
		return
	}
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	if err := b.Extract(req.Path, req.Dest); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.OKMsg(c, "解压完成", nil)
}

func (d *Deps) upload(c *gin.Context) {
	dir := c.Query("path")
	if c.ContentType() != "multipart/form-data" {
		httpx.BadRequest(c, "请使用 multipart/form-data 上传")
		return
	}
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	reader, err := c.Request.MultipartReader()
	if err != nil {
		httpx.BadRequest(c, "解析上传数据失败")
		return
	}
	saved := []gin.H{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			httpx.BadRequest(c, "读取上传数据失败: "+err.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		p, n, err := b.SaveUpload(dir, part.FileName(), part)
		part.Close()
		if err != nil {
			httpx.ServerError(c, "保存 "+part.FileName()+" 失败: "+err.Error())
			return
		}
		saved = append(saved, gin.H{"name": part.FileName(), "path": p, "size": n})
	}
	if len(saved) == 0 {
		httpx.BadRequest(c, "没有收到文件")
		return
	}
	middleware.SetAuditDetail(c, "上传 "+strconv.Itoa(len(saved))+" 个文件到 "+dir)
	httpx.OKMsg(c, "上传完成", gin.H{"items": saved})
}

func (d *Deps) deleteFile(c *gin.Context) {
	p := c.Query("path")
	b, err := d.fileBackend(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	defer b.Close()
	if err := b.Delete(p); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "删除 "+p)
	httpx.OKMsg(c, "已删除", nil)
}

func parseMode(s string, def os.FileMode) os.FileMode {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return def
	}
	return os.FileMode(n)
}
