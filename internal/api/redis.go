package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/middleware"
	"warden/internal/rediscli"
	"warden/internal/secure"
	"warden/internal/store"
)

// redisTimeout 单次 Redis 调用的上限。SCAN 大库时可能偏慢，但必须给个边界。
const redisTimeout = 10 * time.Second

// ---------- 连接配置（控制端保存，按主机隔离） ----------

func (d *Deps) getRedisConn(c *gin.Context) {
	hostID := d.hostIDFromQuery(c)
	var conf store.RedisConf
	if hostID > 0 {
		_ = d.Store.DB.Where("host_id = ?", hostID).First(&conf).Error
	}
	httpx.OK(c, gin.H{
		"host_id":      hostID,
		"addr":         conf.Addr,
		"username":     conf.Username,
		"db":           conf.DB,
		"tls":          conf.TLS,
		"insecure":     conf.Insecure,
		"has_password": conf.Password != "",
		"configured":   strings.TrimSpace(conf.Addr) != "",
	})
}

type redisConnReq struct {
	Addr          string `json:"addr"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	ClearPassword bool   `json:"clear_password"`
	DB            int    `json:"db"`
	TLS           bool   `json:"tls"`
	Insecure      bool   `json:"insecure"`
}

func (d *Deps) saveRedisConn(c *gin.Context) {
	hostID := d.hostIDFromQuery(c)
	if hostID == 0 {
		httpx.BadRequest(c, "主机不存在")
		return
	}
	var req redisConnReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}

	var conf store.RedisConf
	isNew := d.Store.DB.Where("host_id = ?", hostID).First(&conf).Error != nil
	if isNew {
		conf.HostID = hostID
	}

	addr := strings.TrimSpace(req.Addr)
	if addr != "" {
		norm, tlsFromScheme, dbFromURL, err := rediscli.NormalizeAddr(addr)
		if err != nil {
			httpx.BadRequest(c, err.Error())
			return
		}
		addr = norm
		if tlsFromScheme {
			conf.TLS = true
		}
		if dbFromURL >= 0 {
			req.DB = dbFromURL
		}
	} else {
		// 地址清空视为解除配置
		req.DB = 0
		conf.TLS = false
	}
	if req.DB < 0 || req.DB > 255 {
		httpx.BadRequest(c, "数据库编号应在 0-255 之间")
		return
	}
	if req.TLS {
		conf.TLS = true
	}
	conf.Addr = addr
	conf.Username = strings.TrimSpace(req.Username)
	conf.DB = req.DB
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
		httpx.ServerError(c, "保存 Redis 连接失败")
		return
	}
	middleware.SetAuditDetail(c, "保存 Redis 连接 "+conf.Addr)
	httpx.OKMsg(c, "已保存", gin.H{
		"addr":         conf.Addr,
		"username":     conf.Username,
		"db":           conf.DB,
		"tls":          conf.TLS,
		"insecure":     conf.Insecure,
		"has_password": conf.Password != "",
		"configured":   conf.Addr != "",
	})
}

// ---------- 数据接口（本机直接访问，远程经 Agent 转发） ----------

// redisClient 从请求头还原连接参数（由中间件按主机注入）。
// 查询参数 db 允许页面临时切库浏览，不需要每次改连接配置。
func (d *Deps) redisClient(c *gin.Context) (*rediscli.Client, rediscli.Config, bool) {
	cfg := rediscli.ConfigFromHeaders(c.Request.Header)
	if cfg.Empty() {
		httpx.Fail(c, http.StatusPreconditionFailed,
			"该主机尚未配置 Redis 连接，请先在「连接设置」中填写地址")
		return nil, cfg, false
	}
	if v := strings.TrimSpace(c.Query("db")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 255 {
			httpx.BadRequest(c, "数据库编号不合法")
			return nil, cfg, false
		}
		cfg.DB = n
	}
	cl, err := rediscli.New(cfg)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return nil, cfg, false
	}
	return cl, cl.Config(), true
}

// redisStatus 汇总服务端关键指标，同时兼作连接测试。
func (d *Deps) redisStatus(c *gin.Context) {
	cl, cfg, ok := d.redisClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), redisTimeout)
	defer cancel()

	res := gin.H{"online": false, "addr": cfg.Addr, "db": cfg.DB, "tls": cfg.TLS}
	start := time.Now()
	conn, err := cl.Open(ctx)
	if err != nil {
		res["error"] = err.Error()
		httpx.OK(c, res)
		return
	}
	defer conn.Close()
	res["latency_ms"] = time.Since(start).Milliseconds()

	raw, err := conn.String(ctx, "INFO")
	if err != nil {
		res["error"] = err.Error()
		httpx.OK(c, res)
		return
	}
	info := rediscli.ParseInfo(raw)
	res["online"] = true
	res["version"] = info.Get("redis_version")
	res["mode"] = info.Get("redis_mode")
	res["role"] = info.Get("role")
	res["os"] = info.Get("os")
	res["uptime"] = info.GetInt("uptime_in_seconds")
	res["clients"] = info.GetInt("connected_clients")
	res["blocked_clients"] = info.GetInt("blocked_clients")

	res["memory_used"] = info.GetInt("used_memory")
	res["memory_used_human"] = info.Get("used_memory_human")
	res["memory_peak_human"] = info.Get("used_memory_peak_human")
	res["memory_rss_human"] = info.Get("used_memory_rss_human")
	res["maxmemory"] = info.GetInt("maxmemory")
	res["maxmemory_policy"] = info.Get("maxmemory_policy")
	// 碎片率是判断内存是否异常增长的关键指标，直接算好给前端
	used := info.GetInt("used_memory")
	rss := info.GetInt("used_memory_rss")
	if used > 0 && rss > 0 {
		res["mem_fragmentation_ratio"] = float64(rss) / float64(used)
	}

	res["ops"] = info.GetInt("instantaneous_ops_per_sec")
	res["total_commands"] = info.GetInt("total_commands_processed")
	res["hits"] = info.GetInt("keyspace_hits")
	res["misses"] = info.GetInt("keyspace_misses")
	if hits, misses := info.GetInt("keyspace_hits"), info.GetInt("keyspace_misses"); hits+misses > 0 {
		res["hit_rate"] = float64(hits) * 100 / float64(hits+misses)
	}
	res["expired"] = info.GetInt("expired_keys")
	res["evicted"] = info.GetInt("evicted_keys")
	res["rejected"] = info.GetInt("rejected_connections")

	res["aof_enabled"] = info.Get("aof_enabled") == "1"
	res["rdb_last_bgsave_status"] = info.Get("rdb_last_bgsave_status")
	res["persistence"] = info.Get("loading") == "1"
	res["connected_slaves"] = info.GetInt("connected_slaves")

	dbs := info.Keyspace()
	res["dbs"] = dbs
	res["total_keys"] = info.TotalKeys()
	for _, db := range dbs {
		if db.Index == cfg.DB {
			res["keys"] = db.Keys
			res["expires"] = db.Expires
		}
	}
	if _, ok := res["keys"]; !ok {
		res["keys"] = int64(0)
	}
	// 逻辑库数量：Redis 默认 16 个，但 keyspace 只能看到有键的库
	maxIdx := 15
	for _, db := range dbs {
		if db.Index > maxIdx {
			maxIdx = db.Index
		}
	}
	res["databases"] = maxIdx + 1

	httpx.OK(c, res)
}

// redisInfo 返回完整 INFO 分段，用于「服务信息」页签。
func (d *Deps) redisInfo(c *gin.Context) {
	cl, _, ok := d.redisClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), redisTimeout)
	defer cancel()

	conn, err := cl.Open(ctx)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	defer conn.Close()

	raw, err := conn.String(ctx, "INFO")
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	info := rediscli.ParseInfo(raw)
	httpx.OK(c, gin.H{"sections": info.Sections})
}

// redisDBs 列出各逻辑库的键数量，供库切换下拉框展示。
func (d *Deps) redisDBs(c *gin.Context) {
	cl, _, ok := d.redisClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), redisTimeout)
	defer cancel()

	conn, err := cl.Open(ctx)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	defer conn.Close()

	raw, err := conn.String(ctx, "INFO", "keyspace")
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": rediscli.ParseInfo(raw).Keyspace()})
}

func (d *Deps) redisKeys(c *gin.Context) {
	cl, _, ok := d.redisClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), redisTimeout)
	defer cancel()

	conn, err := cl.Open(ctx)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	defer conn.Close()

	count, _ := strconv.Atoi(c.DefaultQuery("count", "2000"))
	page, err := rediscli.ListKeys(ctx, conn, c.Query("pattern"), c.Query("type"), count)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, page)
}

func (d *Deps) redisKeyDetail(c *gin.Context) {
	cl, _, ok := d.redisClient(c)
	if !ok {
		return
	}
	key := strings.TrimSpace(c.Query("key"))
	if key == "" {
		httpx.BadRequest(c, "请指定键名")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), redisTimeout)
	defer cancel()

	conn, err := cl.Open(ctx)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	defer conn.Close()

	detail, err := rediscli.GetKey(ctx, conn, key)
	if err == rediscli.ErrNotExist {
		httpx.NotFound(c, "键不存在（可能刚好过期被清理）")
		return
	}
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(c, detail)
}

type redisKeyReq struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Field string `json:"field"`
	Value string `json:"value"`
	TTL   int    `json:"ttl"`
}

// redisSetKey 新建或更新键。字符串整体覆盖，hash/list/set/zset 只追加单条，
// 避免误操作把已有集合清空。
func (d *Deps) redisSetKey(c *gin.Context) {
	cl, _, ok := d.redisClient(c)
	if !ok {
		return
	}
	var req redisKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		httpx.BadRequest(c, "请填写键名")
		return
	}
	if key == "*" {
		httpx.BadRequest(c, "键名不能是 *")
		return
	}
	typ := strings.ToLower(strings.TrimSpace(req.Type))
	if typ == "" {
		typ = "string"
	}
	if req.TTL < 0 {
		httpx.BadRequest(c, "过期时间不能为负数")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), redisTimeout)
	defer cancel()

	conn, err := cl.Open(ctx)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	defer conn.Close()

	if typ == "string" {
		err = rediscli.SetString(ctx, conn, key, req.Value, req.TTL)
	} else {
		err = rediscli.SetField(ctx, conn, key, typ, req.Field, req.Value, req.TTL)
	}
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "写入 Redis 键 "+key+" ("+typ+")")
	httpx.OKMsg(c, "已写入", gin.H{"key": key, "type": typ})
}

func (d *Deps) redisDeleteKey(c *gin.Context) {
	cl, _, ok := d.redisClient(c)
	if !ok {
		return
	}
	key := strings.TrimSpace(c.Query("key"))
	if key == "" {
		httpx.BadRequest(c, "请指定键名")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), redisTimeout)
	defer cancel()

	conn, err := cl.Open(ctx)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	defer conn.Close()

	n, err := rediscli.DeleteKey(ctx, conn, key)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	middleware.SetAuditDetail(c, "删除 Redis 键 "+key)
	httpx.OKMsg(c, "已删除", gin.H{"deleted": n})
}

type redisTTLReq struct {
	Key string `json:"key"`
	TTL int    `json:"ttl"` // <=0 表示移除过期时间
}

func (d *Deps) redisExpireKey(c *gin.Context) {
	cl, _, ok := d.redisClient(c)
	if !ok {
		return
	}
	var req redisTTLReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		httpx.BadRequest(c, "请指定键名")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), redisTimeout)
	defer cancel()

	conn, err := cl.Open(ctx)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	defer conn.Close()

	changed, err := rediscli.ExpireKey(ctx, conn, key, req.TTL)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	action := "设置过期时间"
	if req.TTL <= 0 {
		action = "移除过期时间"
	}
	middleware.SetAuditDetail(c, action+" "+key)
	httpx.OKMsg(c, action+"成功", gin.H{"changed": changed})
}

// ---------- 命令台 ----------

// redisBlockedCommands 拦住几类「一条命令就能让业务全线停摆」的操作。
// 面板定位是日常运维，这类操作让人上服务器敲更安全，也避免审计里出现事故。
var redisBlockedCommands = map[string]string{
	"shutdown":  "停库命令会直接中断业务",
	"flushall":  "会清空所有数据库的数据",
	"flushdb":   "会清空当前数据库的数据",
	"swapdb":    "会交换两个库的数据",
	"monitor":   "会持续占用连接直到超时",
	"debug":     "调试命令可能导致进程崩溃",
	"module":    "会加载或卸载服务端模块",
	"migrate":   "会把手上的键搬到其它实例",
	"replicaof": "会改变主从关系",
	"slaveof":   "会改变主从关系",
	"failover":  "会触发主从切换",
	"psync":     "复制协议内部命令",
	"sync":      "复制协议内部命令",
}

// redisBlockedSub 拦住危险子命令，键为「命令 子命令」。
var redisBlockedSub = map[string]string{
	"config set":     "修改运行参数可能让服务立刻不可用",
	"config rewrite": "会改写服务端配置文件",
	"acl setuser":    "会改动账号权限",
	"acl deluser":    "会删除账号",
}

func checkRedisCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("请输入命令")
	}
	cmd := strings.ToLower(args[0])
	if reason, hit := redisBlockedCommands[cmd]; hit {
		return fmt.Errorf("面板已禁用 %s 命令：%s，请登录服务器执行", strings.ToUpper(cmd), reason)
	}
	if len(args) > 1 {
		if reason, hit := redisBlockedSub[cmd+" "+strings.ToLower(args[1])]; hit {
			return fmt.Errorf("面板已禁用 %s %s：%s，请登录服务器执行",
				strings.ToUpper(args[0]), strings.ToUpper(args[1]), reason)
		}
	}
	return nil
}

type redisCmdReq struct {
	Command string `json:"command"`
}

func (d *Deps) redisCommand(c *gin.Context) {
	cl, _, ok := d.redisClient(c)
	if !ok {
		return
	}
	var req redisCmdReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "参数解析失败")
		return
	}
	args, err := rediscli.ParseCommandLine(req.Command)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	strs := make([]string, 0, len(args))
	for _, a := range args {
		strs = append(strs, rediscli.ReplyString(a))
	}
	if err := checkRedisCommand(strs); err != nil {
		httpx.Forbidden(c, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), redisTimeout)
	defer cancel()

	conn, err := cl.Open(ctx)
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, err.Error())
		return
	}
	defer conn.Close()

	start := time.Now()
	reply, err := conn.Do(ctx, args...)
	cost := time.Since(start).Milliseconds()
	if err != nil {
		// 命令语法错误属于正常使用场景，原样带回给前端展示，不算服务端故障
		httpx.OK(c, gin.H{
			"ok":      false,
			"output":  rediscli.Format(err),
			"cost_ms": cost,
			"command": req.Command,
		})
		return
	}
	// 写命令留痕，只记命令名与首个参数，避免把敏感值写进审计
	if isRedisWrite(strs[0]) {
		target := strings.ToUpper(strs[0])
		if len(strs) > 1 {
			target += " " + strs[1]
		}
		middleware.SetAuditDetail(c, "执行 Redis 命令 "+target)
	}
	httpx.OK(c, gin.H{
		"ok":      true,
		"output":  rediscli.Format(reply),
		"cost_ms": cost,
		"command": req.Command,
	})
}

// isRedisWrite 判断是否写命令（用于审计留痕）。
func isRedisWrite(cmd string) bool {
	switch strings.ToLower(cmd) {
	case "set", "setex", "setnx", "mset", "del", "unlink", "expire", "persist",
		"hset", "hmset", "hdel", "lpush", "rpush", "lpop", "rpop", "sadd", "srem",
		"zadd", "zrem", "incr", "decr", "rename", "restore", "eval", "evalsha",
		"xadd", "xdel", "xtrim", "expireat", "getset", "append":
		return true
	}
	return false
}
