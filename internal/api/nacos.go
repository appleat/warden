package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/nacos"
	"warden/internal/secure"
	"warden/internal/store"
)

// nacosTimeout 给单次 Nacos 调用留出足够时间，又不至于把浏览器卡死。
const nacosTimeout = 12 * time.Second

// ---------- 连接配置（控制端保存，按主机隔离） ----------

func (d *Deps) hostIDFromQuery(c *gin.Context) uint {
	raw := strings.TrimSpace(c.Query("host"))
	if raw == "" || raw == "local" || raw == "0" {
		if h := d.Fleet.Local(); h != nil {
			return h.ID
		}
		return 0
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}

func (d *Deps) getNacosConn(c *gin.Context) {
	hostID := d.hostIDFromQuery(c)
	var conf store.NacosConf
	if hostID > 0 {
		_ = d.Store.DB.Where("host_id = ?", hostID).First(&conf).Error
	}
	httpx.OK(c, gin.H{
		"host_id":      hostID,
		"addr":         conf.Addr,
		"username":     conf.Username,
		"namespace":    conf.Namespace,
		"insecure":     conf.Insecure,
		"has_password": conf.Password != "",
		"configured":   strings.TrimSpace(conf.Addr) != "",
	})
}

type nacosConnReq struct {
	Addr          string `json:"addr"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	ClearPassword bool   `json:"clear_password"`
	Namespace     string `json:"namespace"`
	Insecure      bool   `json:"insecure"`
}

func (d *Deps) saveNacosConn(c *gin.Context) {
	hostID := d.hostIDFromQuery(c)
	if hostID == 0 {
		httpx.BadRequest(c, "主机不存在")
		return
	}
	var req nacosConnReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}

	var conf store.NacosConf
	isNew := d.Store.DB.Where("host_id = ?", hostID).First(&conf).Error != nil
	if isNew {
		conf.HostID = hostID
	}

	addr := strings.TrimSpace(req.Addr)
	if addr != "" {
		norm, err := nacos.NormalizeAddr(addr)
		if err != nil {
			httpx.BadRequest(c, err.Error())
			return
		}
		addr = norm
	}
	conf.Addr = addr
	conf.Username = strings.TrimSpace(req.Username)
	conf.Namespace = strings.TrimSpace(req.Namespace)
	conf.Insecure = req.Insecure

	switch {
	case req.ClearPassword:
		conf.Password = ""
	case strings.TrimSpace(req.Password) != "":
		enc, err := secure.Encrypt(d.Cfg.SecretKey(), req.Password)
		if err != nil {
			httpx.ServerError(c, "凭据加密失败")
			return
		}
		conf.Password = enc
	}

	var err error
	if isNew {
		err = d.Store.DB.Create(&conf).Error
	} else {
		err = d.Store.DB.Save(&conf).Error
	}
	if err != nil {
		httpx.ServerError(c, "保存 Nacos 连接失败")
		return
	}
	middleware.SetAuditDetail(c, "保存 Nacos 连接 "+conf.Addr)
	httpx.OKMsg(c, "已保存", gin.H{
		"addr":         conf.Addr,
		"username":     conf.Username,
		"namespace":    conf.Namespace,
		"insecure":     conf.Insecure,
		"has_password": conf.Password != "",
		"configured":   conf.Addr != "",
	})
}

// ---------- 数据接口（本机直接访问，远程经 Agent 转发） ----------

// nacosClient 从请求头还原连接参数。参数由中间件按主机注入。
func (d *Deps) nacosClient(c *gin.Context) (*nacos.Client, nacos.Config, bool) {
	cfg := nacos.ConfigFromHeaders(c.Request.Header)
	if cfg.Empty() {
		httpx.Fail(c, http.StatusPreconditionFailed,
			"该主机尚未配置 Nacos 连接，请先在「连接设置」中填写地址")
		return nil, cfg, false
	}
	cl, err := nacos.New(cfg)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return nil, cfg, false
	}
	return cl, cfg, true
}

// namespaceOf 读取请求里的命名空间。空串代表 public 命名空间，
// 这里不做「回落到默认命名空间」的兜底，避免前端切到 public 时被默认值覆盖。
func namespaceOf(c *gin.Context) string {
	return strings.TrimSpace(c.Query("namespace"))
}

func (d *Deps) nacosStatus(c *gin.Context) {
	cl, cfg, ok := d.nacosClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), nacosTimeout)
	defer cancel()

	res := gin.H{"online": false, "addr": cfg.Addr, "namespace": cfg.Namespace}
	start := time.Now()
	if err := cl.Ping(ctx); err != nil {
		res["error"] = err.Error()
		httpx.OK(c, res)
		return
	}
	res["online"] = true
	res["latency_ms"] = time.Since(start).Milliseconds()

	if st, err := cl.State(ctx); err == nil && st != nil {
		res["version"] = st.Version
		res["mode"] = st.StartupMode
		res["auth_enabled"] = st.AuthEnabled
		res["datasource"] = st.Datasource
		res["server_env"] = st.ServerEnv
	}
	if m, err := cl.Metrics(ctx); err == nil && m != nil {
		res["service_count"] = m.ServiceCount
		res["instance_count"] = m.InstanceCount
		res["cpu"] = m.CPU
		res["load"] = m.Load
	}
	httpx.OK(c, res)
}

func (d *Deps) nacosNamespaces(c *gin.Context) {
	cl, _, ok := d.nacosClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), nacosTimeout)
	defer cancel()
	items, err := cl.Namespaces(ctx)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items})
}

func (d *Deps) nacosServices(c *gin.Context) {
	cl, _, ok := d.nacosClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), nacosTimeout)
	defer cancel()

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	group := strings.TrimSpace(c.Query("group"))
	keyword := strings.TrimSpace(c.Query("keyword"))

	list, err := cl.Services(ctx, namespaceOf(c), group, keyword, page, size)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{
		"items":  list.Items,
		"total":  list.Total,
		"page":   page,
		"size":   size,
		"source": list.Source,
		"hint":   list.Hint,
	})
}

func (d *Deps) nacosInstances(c *gin.Context) {
	cl, _, ok := d.nacosClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), nacosTimeout)
	defer cancel()

	group := strings.TrimSpace(c.Query("group"))
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	res, err := cl.Instances(ctx, namespaceOf(c), group, c.Query("name"), c.Query("healthy") == "true")
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, res)
}

func (d *Deps) nacosConfigs(c *gin.Context) {
	cl, _, ok := d.nacosClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), nacosTimeout)
	defer cancel()

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	items, total, err := cl.Configs(ctx, namespaceOf(c), strings.TrimSpace(c.Query("group")),
		strings.TrimSpace(c.Query("data_id")), page, size)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": items, "total": total, "page": page, "size": size})
}

// nacosDiagnose 逐个探测面板用到的 Nacos 接口，返回状态码与返回片段，
// 方便排查不同 Nacos 版本（1.x / 2.x / 3.x）的兼容性差异。
func (d *Deps) nacosDiagnose(c *gin.Context) {
	cl, _, ok := d.nacosClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), nacosTimeout)
	defer cancel()
	probes := cl.Diagnose(ctx, namespaceOf(c))
	httpx.OK(c, gin.H{"items": probes})
}

func (d *Deps) nacosConfigDetail(c *gin.Context) {
	cl, _, ok := d.nacosClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), nacosTimeout)
	defer cancel()

	group := strings.TrimSpace(c.Query("group"))
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	dataID := strings.TrimSpace(c.Query("data_id"))
	content, err := cl.ConfigContent(ctx, namespaceOf(c), group, dataID)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{"data_id": dataID, "group": group, "content": content})
}

type nacosConfigReq struct {
	Namespace string `json:"namespace"`
	Group     string `json:"group"`
	DataID    string `json:"data_id"`
	Content   string `json:"content"`
	Type      string `json:"type"`
}

func (d *Deps) nacosPublishConfig(c *gin.Context) {
	cl, _, ok := d.nacosClient(c)
	if !ok {
		return
	}
	var req nacosConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	// 空命名空间代表 public，不做默认值兜底，避免写到错误的命名空间
	ns := strings.TrimSpace(req.Namespace)
	group := strings.TrimSpace(req.Group)
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	dataID := strings.TrimSpace(req.DataID)
	if dataID == "" {
		httpx.BadRequest(c, "请填写 Data ID")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), nacosTimeout)
	defer cancel()
	if err := cl.PublishConfig(ctx, ns, group, dataID, req.Content, req.Type); err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "发布配置 "+group+"/"+dataID)
	httpx.OKMsg(c, "配置已发布", gin.H{"data_id": dataID, "group": group})
}

func (d *Deps) nacosDeleteConfig(c *gin.Context) {
	cl, _, ok := d.nacosClient(c)
	if !ok {
		return
	}
	group := strings.TrimSpace(c.Query("group"))
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	dataID := strings.TrimSpace(c.Query("data_id"))
	if dataID == "" {
		httpx.BadRequest(c, "请选择配置")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), nacosTimeout)
	defer cancel()
	if err := cl.DeleteConfig(ctx, namespaceOf(c), group, dataID); err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "删除配置 "+group+"/"+dataID)
	httpx.OKMsg(c, "配置已删除", nil)
}
