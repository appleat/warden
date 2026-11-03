package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"warden/internal/fleet"
	"warden/internal/mysqldb"
	"warden/internal/secure"
	"warden/internal/store"
)

// runTimeout 是单次备份的上限。整库备份本来就慢，给足时间，
// 但不能无限期挂着——否则一个卡死的连接会永远占着任务。
const runTimeout = 6 * time.Hour

// tickInterval 是调度扫描间隔。备份精度到分钟，10 秒扫一次足够，
// 也让「改完计划立刻生效」不需要等太久。
const tickInterval = 10 * time.Second

// ErrRunning 表示该任务正在备份中。
var ErrRunning = errors.New("该任务正在备份中，请稍候")

// File 是一个备份文件。
type File struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// openDB 抽成变量，测试里可以换成假驱动；正式运行永远指向 mysqldb.Open。
var openDB = mysqldb.Open

// Service 是 MySQL 定时备份的调度与执行器。
//
// 任务本身只存「备哪台主机上的哪个库」，数据库账号密码仍然只有 MySQLConf 一份，
// 执行时现取现用——改密码不需要回头改任务。
type Service struct {
	st   *store.Store
	reg  *fleet.Registry
	key  []byte
	root string

	mu      sync.Mutex
	running map[uint]bool
}

// New 创建备份服务并确保根目录存在。
func New(st *store.Store, reg *fleet.Registry, key []byte, root string) (*Service, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("创建备份目录失败：%w", err)
	}
	return &Service{
		st:      st,
		reg:     reg,
		key:     key,
		root:    root,
		running: map[uint]bool{},
	}, nil
}

// Root 返回备份根目录（用于界面展示）。
func (s *Service) Root() string { return s.root }

// Start 启动后台调度。
func (s *Service) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		s.loop(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.loop(ctx)
			}
		}
	}()
}

// loop 扫描到期的任务。先推进 next_run_at 再执行，
// 避免一次备份跑得比扫描间隔还久时被反复触发。
func (s *Service) loop(ctx context.Context) {
	var list []store.MySQLBackup
	if err := s.st.DB.Where("enabled = ?", true).Find(&list).Error; err != nil {
		slog.Warn("读取备份任务失败", "err", err)
		return
	}
	now := time.Now()
	for i := range list {
		task := list[i]
		if task.NextRunAt == nil {
			// 老数据或手工改库留下的空值：补一个下次时间，不立刻跑
			s.setNextRun(task.ID, NextRun(&task, now))
			continue
		}
		if task.NextRunAt.After(now) {
			continue
		}
		s.setNextRun(task.ID, NextRun(&task, now))
		if err := s.RunAsync(task.ID); err != nil {
			slog.Warn("触发定时备份失败", "task", task.ID, "name", task.Name, "err", err)
		}
	}
	_ = ctx
}

func (s *Service) setNextRun(id uint, t time.Time) {
	s.st.DB.Model(&store.MySQLBackup{}).Where("id = ?", id).Update("next_run_at", t)
}

// Running 报告任务当前是否在跑。
func (s *Service) Running(id uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}

// claim 抢占任务，防止定时与手动同时跑同一个任务。
func (s *Service) claim(id uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Service) release(id uint) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

// RunAsync 异步执行一次备份（定时触发与「立即备份」共用）。
func (s *Service) RunAsync(id uint) error {
	var task store.MySQLBackup
	if err := s.st.DB.First(&task, id).Error; err != nil {
		return fmt.Errorf("任务不存在")
	}
	if !s.claim(id) {
		return ErrRunning
	}
	go func() {
		defer s.release(id)
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		s.run(ctx, &task)
	}()
	return nil
}

// RunSync 同步执行一次备份，供测试与命令行使用。
func (s *Service) RunSync(ctx context.Context, id uint) error {
	var task store.MySQLBackup
	if err := s.st.DB.First(&task, id).Error; err != nil {
		return fmt.Errorf("任务不存在")
	}
	if !s.claim(id) {
		return ErrRunning
	}
	defer s.release(id)
	s.run(ctx, &task)
	return nil
}

// run 执行一次备份并回写结果。
func (s *Service) run(ctx context.Context, task *store.MySQLBackup) {
	start := time.Now()
	s.st.DB.Model(&store.MySQLBackup{}).Where("id = ?", task.ID).Updates(map[string]any{
		"last_status": "running",
		"last_error":  "",
	})

	name, size, rows, err := s.execute(ctx, task)

	upd := map[string]any{
		"last_run_at": start,
		"last_status": "ok",
		"last_error":  "",
		"last_file":   name,
		"last_size":   size,
		"last_rows":   rows,
	}
	if err != nil {
		upd["last_status"] = "failed"
		upd["last_error"] = truncate(err.Error(), 900)
		upd["last_file"] = ""
		upd["last_size"] = int64(0)
		upd["last_rows"] = int64(0)
		slog.Warn("MySQL 备份失败", "task", task.ID, "name", task.Name,
			"schema", task.Schema, "err", err)
	} else {
		slog.Info("MySQL 备份完成", "task", task.ID, "name", task.Name,
			"schema", task.Schema, "file", name, "size", size, "rows", rows,
			"cost_ms", time.Since(start).Milliseconds())
	}
	s.st.DB.Model(&store.MySQLBackup{}).Where("id = ?", task.ID).Updates(upd)
}

// execute 生成备份文件，返回文件名、字节数、行数。
func (s *Service) execute(ctx context.Context, task *store.MySQLBackup) (string, int64, int64, error) {
	var conf store.MySQLConf
	if err := s.st.DB.Where("host_id = ?", task.HostID).First(&conf).Error; err != nil {
		return "", 0, 0, fmt.Errorf("该主机还没有配置 MySQL 连接，请先在「连接设置」里填好地址与账号")
	}
	if strings.TrimSpace(conf.Host) == "" {
		return "", 0, 0, fmt.Errorf("该主机的 MySQL 连接地址为空，请先在「连接设置」里补全")
	}
	pass, err := secure.Decrypt(s.key, conf.Password)
	if err != nil {
		return "", 0, 0, fmt.Errorf("解密数据库密码失败：%w", err)
	}
	cfg := mysqldb.Config{
		Host:     conf.Host,
		Port:     conf.Port,
		Username: conf.Username,
		Password: pass,
		Database: conf.Database,
		Charset:  conf.Charset,
		TLS:      conf.TLS,
		Insecure: conf.Insecure,
		ReadOnly: conf.ReadOnly,
	}

	dir := s.TaskDir(task.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, 0, fmt.Errorf("创建备份目录失败：%w", err)
	}

	name := uniqueFileName(dir, FileName(task, time.Now()))
	tmp := filepath.Join(dir, "."+name+".part")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, 0, fmt.Errorf("创建备份文件失败：%w", err)
	}

	var w io.Writer = f
	var gz *gzip.Writer
	if task.Compress {
		gz = gzip.NewWriter(f)
		w = gz
	}

	stat, dumpErr := s.dump(ctx, task, cfg, w)
	if gz != nil {
		if err := gz.Close(); err != nil && dumpErr == nil {
			dumpErr = fmt.Errorf("写入备份文件失败：%w", err)
		}
	}
	if err := f.Sync(); err != nil && dumpErr == nil {
		dumpErr = fmt.Errorf("写入备份文件失败：%w", err)
	}
	f.Close()

	if dumpErr != nil {
		// 失败的半成品不能留在目录里，否则会被当成一份可用的备份
		os.Remove(tmp)
		return "", 0, 0, dumpErr
	}

	final := filepath.Join(dir, name)
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", 0, 0, fmt.Errorf("保存备份文件失败：%w", err)
	}

	var size int64
	if fi, err := os.Stat(final); err == nil {
		size = fi.Size()
	}
	s.prune(task)
	return name, size, stat.Rows, nil
}

// dump 按主机类型选择执行位置：本机直连数据库，远程交给该主机的 Agent。
func (s *Service) dump(ctx context.Context, task *store.MySQLBackup, cfg mysqldb.Config, w io.Writer) (*mysqldb.DumpStat, error) {
	opt := mysqldb.DumpOptions{
		Schema:       task.Schema,
		Tables:       task.Tables,
		WithDatabase: true,
	}
	if s.isRemote(task.HostID) {
		return s.dumpViaAgent(ctx, task.HostID, cfg, opt, w)
	}
	db, err := openDB(cfg)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return mysqldb.Dump(ctx, db, opt, w)
}

func (s *Service) isRemote(hostID uint) bool {
	if s.reg == nil {
		return false
	}
	h, err := s.reg.Get(hostID)
	if err != nil || h == nil {
		return false
	}
	return !s.reg.IsLocal(h)
}

// dumpViaAgent 让目标主机的 Agent 执行导出，SQL 流回控制端落盘。
// 这样数据库地址是 127.0.0.1 时也能备份——由 Agent 在其所在网络连库。
func (s *Service) dumpViaAgent(ctx context.Context, hostID uint, cfg mysqldb.Config, opt mysqldb.DumpOptions, w io.Writer) (*mysqldb.DumpStat, error) {
	h, err := s.reg.Get(hostID)
	if err != nil || h == nil {
		return nil, fmt.Errorf("主机不存在或已被删除")
	}
	base, err := s.reg.BaseURL(h)
	if err != nil {
		return nil, fmt.Errorf("主机地址不可用：%w", err)
	}

	body, err := json.Marshal(opt)
	if err != nil {
		return nil, err
	}
	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + "/agent/api/mysql/backup/dump"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", s.reg.AgentToken(h))
	// 数据库连接参数照旧走请求头下发，Agent 不落库
	mysqldb.SetHeaders(req.Header, cfg)

	resp, err := s.reg.HTTPClient(h).Do(req)
	if err != nil {
		return nil, fmt.Errorf("主机【%s】不可达：%w", h.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("主机【%s】执行备份失败：%s", h.Name, errorText(raw, resp.StatusCode))
	}

	cw := &countingWriter{w: w}
	if _, err := io.Copy(cw, resp.Body); err != nil {
		return nil, fmt.Errorf("接收备份数据中断：%w", err)
	}
	// 行数由 Agent 通过 HTTP trailer 带回；取不到就当未知，不影响备份本身
	rows, _ := strconv.ParseInt(resp.Trailer.Get("X-Dump-Rows"), 10, 64)
	return &mysqldb.DumpStat{Rows: rows, Bytes: cw.n}, nil
}

// errorText 从 Agent 的 JSON 错误响应里取出 msg，取不到就原样返回。
func errorText(raw []byte, status int) string {
	var payload struct {
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil && payload.Msg != "" {
		return payload.Msg
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	return truncate(s, 300)
}

// ---------- 文件 ----------

// TaskDir 返回某个任务的备份目录。
func (s *Service) TaskDir(taskID uint) string {
	return filepath.Join(s.root, "mysql", fmt.Sprintf("task-%d", taskID))
}

// Files 列出某个任务的备份文件，最新的在前。
func (s *Service) Files(taskID uint) ([]File, error) {
	entries, err := os.ReadDir(s.TaskDir(taskID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []File{}, nil
		}
		return nil, err
	}
	out := make([]File, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, File{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// FilePath 返回备份文件的绝对路径，name 只能是目录下的文件名。
func (s *Service) FilePath(taskID uint, name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("文件名不合法")
	}
	p := filepath.Join(s.TaskDir(taskID), name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("备份文件不存在")
	}
	return p, nil
}

// DeleteFile 删除单个备份文件。
func (s *Service) DeleteFile(taskID uint, name string) error {
	p, err := s.FilePath(taskID, name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// RemoveTask 删除任务时一并清掉它的备份目录。
func (s *Service) RemoveTask(taskID uint) error {
	return os.RemoveAll(s.TaskDir(taskID))
}

// prune 按保留份数清理旧备份。Keep<=0 表示不清理。
func (s *Service) prune(task *store.MySQLBackup) {
	if task.Keep <= 0 {
		return
	}
	files, err := s.Files(task.ID)
	if err != nil || len(files) <= task.Keep {
		return
	}
	for _, f := range files[task.Keep:] {
		if err := os.Remove(filepath.Join(s.TaskDir(task.ID), f.Name)); err != nil {
			slog.Warn("清理旧备份失败", "task", task.ID, "file", f.Name, "err", err)
		}
	}
}

// ---------- 命名 ----------

// FileName 生成备份文件名：库名 + 范围 + 时间戳，扩展名按是否压缩。
// 名字里带上库名与时间，下载后一眼能看出是哪一份。
func FileName(task *store.MySQLBackup, now time.Time) string {
	scope := "full"
	if n := len(task.Tables); n > 0 {
		scope = fmt.Sprintf("%dtables", n)
	}
	ext := ".sql"
	if task.Compress {
		ext = ".sql.gz"
	}
	return fmt.Sprintf("%s_%s_%s%s", sanitize(task.Schema), scope, now.Format("20060102-150405"), ext)
}

// uniqueFileName 避免同一秒内连续备份互相覆盖（手动触发与定时撞在一起时会发生）。
func uniqueFileName(dir, name string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := os.Stat(filepath.Join(dir, cand)); err != nil {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext)
}

// sanitize 只保留文件名安全字符，避免库名里的特殊字符影响落盘。
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		return "db"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// countingWriter 统计写出字节数。
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
