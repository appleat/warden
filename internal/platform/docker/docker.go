package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// Service 封装 Docker Engine API。Client 懒连接，Docker 不可用时不影响其他模块。
type Service struct {
	host string

	mu  sync.Mutex
	cli *client.Client
}

func New(host string) *Service {
	return &Service{host: host}
}

func (s *Service) Client() (*client.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cli != nil {
		return s.cli, nil
	}
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if s.host != "" {
		opts = append(opts, client.WithHost(s.host))
	} else {
		opts = append(opts, client.FromEnv)
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("初始化 Docker 客户端失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "cannot find the file") || strings.Contains(msg, "no such file"):
			return nil, errors.New("未检测到运行中的 Docker。Linux 请确认 `systemctl status docker` 正常且 /var/run/docker.sock 存在")
		case strings.Contains(msg, "permission denied") || strings.Contains(msg, "Access is denied"):
			return nil, errors.New("无权限访问 Docker socket，请将当前用户加入 docker 组，或以 root 运行本服务")
		case strings.Contains(msg, "connection refused"):
			return nil, errors.New("Docker 守护进程未监听，请先启动 Docker 服务")
		}
		return nil, fmt.Errorf("无法连接 Docker 守护进程: %s", msg)
	}
	s.cli = cli
	return cli, nil
}

// Status 供前端展示 Docker 可用性。
type Status struct {
	Available   bool   `json:"available"`
	Version     string `json:"version"`
	APIVersion  string `json:"api_version"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Containers  int    `json:"containers"`
	Running     int    `json:"running"`
	Images      int    `json:"images"`
	Error       string `json:"error"`
	ServerEnd   string `json:"endpoint"`
	DockerReady bool   `json:"docker_ready"`
}

func (s *Service) Status(ctx context.Context) Status {
	st := Status{}
	cli, err := s.Client()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if v, err := cli.ServerVersion(ctx); err == nil {
		st.Version = v.Version
		st.APIVersion = v.APIVersion
		st.OS = v.Os
		st.Arch = v.Arch
		st.Available = true
		st.DockerReady = true
	}
	if list, err := cli.ContainerList(ctx, container.ListOptions{All: true}); err == nil {
		st.Containers = len(list)
		for _, c := range list {
			if c.State == "running" {
				st.Running++
			}
		}
	}
	if imgs, err := cli.ImageList(ctx, imageListOptions()); err == nil {
		st.Images = len(imgs)
	}
	return st
}

// ---------- 容器 ----------

type PortItem struct {
	IP          string `json:"ip"`
	PrivatePort uint16 `json:"private_port"`
	PublicPort  uint16 `json:"public_port"`
	Type        string `json:"type"`
}

type ContainerItem struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	ImageID string            `json:"image_id"`
	Command string            `json:"command"`
	State   string            `json:"state"`
	Status  string            `json:"status"`
	Created int64             `json:"created"`
	Ports   []PortItem        `json:"ports"`
	Labels  map[string]string `json:"labels"`
	Project string            `json:"project"`

	// 实时资源占用，仅在请求带 stats=true 且容器 running 时填充
	CPUPercent float64 `json:"cpu_percent"`
	MemUsage   uint64  `json:"mem_usage"`
	MemLimit   uint64  `json:"mem_limit"`
	MemPercent float64 `json:"mem_percent"`
	// 采集失败的原因。为空表示数据可信；非空时前端应显示为「未知」而不是 0。
	StatsError string `json:"stats_error"`
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// dedupePorts 把同一端口在 IPv4 / IPv6 上的重复条目合并成一条。
// Docker 的容器列表会按 (IP, 端口) 逐条返回，不去重时界面上一堆相同端口。
func dedupePorts(src []container.Port) []PortItem {
	out := make([]PortItem, 0, len(src))
	seen := make(map[string]struct{}, len(src))
	for _, p := range src {
		key := fmt.Sprintf("%d/%d/%s", p.PublicPort, p.PrivatePort, p.Type)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, PortItem{IP: p.IP, PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Type: p.Type})
	}
	return out
}

func (s *Service) List(ctx context.Context, all bool, keyword string) ([]ContainerItem, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, err
	}
	opts := container.ListOptions{All: all}
	list, err := cli.ContainerList(ctx, opts)
	if err != nil {
		return nil, err
	}
	kw := strings.ToLower(strings.TrimSpace(keyword))
	out := make([]ContainerItem, 0, len(list))
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		if kw != "" {
			if !strings.Contains(strings.ToLower(name), kw) &&
				!strings.Contains(strings.ToLower(c.Image), kw) &&
				!strings.Contains(strings.ToLower(c.ID), kw) {
				continue
			}
		}
		ports := dedupePorts(c.Ports)
		out = append(out, ContainerItem{
			ID: c.ID, Name: name, Image: c.Image, ImageID: c.ImageID,
			Command: c.Command, State: c.State, Status: c.Status,
			Created: c.Created, Ports: ports, Labels: c.Labels,
			Project: c.Labels["com.docker.compose.project"],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := out[i].State == "running", out[j].State == "running"
		if ri != rj {
			return ri
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Service) Inspect(ctx context.Context, id string) (json.RawMessage, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, err
	}
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

func (s *Service) Start(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return err
	}
	return cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (s *Service) Stop(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return err
	}
	return cli.ContainerStop(ctx, id, container.StopOptions{Timeout: ptr(10)})
}

func (s *Service) Restart(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return err
	}
	return cli.ContainerRestart(ctx, id, container.StopOptions{Timeout: ptr(10)})
}

func (s *Service) Pause(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return err
	}
	return cli.ContainerPause(ctx, id)
}

func (s *Service) Unpause(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return err
	}
	return cli.ContainerUnpause(ctx, id)
}

func (s *Service) Remove(ctx context.Context, id string, force bool) error {
	cli, err := s.Client()
	if err != nil {
		return err
	}
	return cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: force, RemoveVolumes: false})
}

type PortSpec struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Proto         string `json:"proto"`
}

type BindSpec struct {
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only"`
}

type CreateSpec struct {
	Name          string     `json:"name"`
	Image         string     `json:"image"`
	Cmd           string     `json:"cmd"`
	Env           []string   `json:"env"`
	Ports         []PortSpec `json:"ports"`
	Binds         []BindSpec `json:"binds"`
	RestartPolicy string     `json:"restart_policy"`
	AutoRemove    bool       `json:"auto_remove"`
	AutoPull      bool       `json:"auto_pull"`
	NetworkMode   string     `json:"network_mode"`
	WorkDir       string     `json:"work_dir"`
	Labels        []string   `json:"labels"`
}

func (s *Service) Create(ctx context.Context, spec CreateSpec) (string, error) {
	cli, err := s.Client()
	if err != nil {
		return "", err
	}
	img := strings.TrimSpace(spec.Image)
	if img == "" {
		return "", errors.New("镜像不能为空")
	}
	if spec.AutoPull {
		if err := s.PullQuiet(ctx, img); err != nil {
			return "", fmt.Errorf("拉取镜像失败: %w", err)
		}
	}

	cfg := &container.Config{Image: img, Env: spec.Env}
	if len(spec.Env) == 0 {
		cfg.Env = nil
	}
	if spec.Cmd != "" {
		cfg.Cmd = splitCommand(spec.Cmd)
	}
	if spec.WorkDir != "" {
		cfg.WorkingDir = spec.WorkDir
	}
	if len(spec.Labels) > 0 {
		cfg.Labels = map[string]string{}
		for _, l := range spec.Labels {
			if k, v, ok := strings.Cut(l, "="); ok {
				cfg.Labels[k] = v
			}
		}
	}

	hostCfg := &container.HostConfig{AutoRemove: spec.AutoRemove}
	if spec.RestartPolicy != "" && spec.RestartPolicy != "no" {
		hostCfg.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(spec.RestartPolicy)}
	}
	if spec.NetworkMode != "" {
		hostCfg.NetworkMode = container.NetworkMode(spec.NetworkMode)
	}

	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range spec.Ports {
		if p.ContainerPort <= 0 {
			continue
		}
		proto := p.Proto
		if proto == "" {
			proto = "tcp"
		}
		np := nat.Port(fmt.Sprintf("%d/%s", p.ContainerPort, proto))
		exposed[np] = struct{}{}
		if p.HostPort > 0 {
			bindings[np] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", p.HostPort)}}
		}
	}
	if len(exposed) > 0 {
		cfg.ExposedPorts = exposed
		hostCfg.PortBindings = bindings
	}
	for _, b := range spec.Binds {
		if b.HostPath == "" || b.ContainerPath == "" {
			continue
		}
		mode := "rw"
		if b.ReadOnly {
			mode = "ro"
		}
		hostCfg.Binds = append(hostCfg.Binds, fmt.Sprintf("%s:%s:%s", b.HostPath, b.ContainerPath, mode))
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, spec.Name)
	if err != nil {
		return "", err
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return resp.ID, fmt.Errorf("容器已创建但启动失败: %w", err)
	}
	return resp.ID, nil
}

func splitCommand(s string) []string {
	// 简单解析：支持引号包裹的片段
	var out []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// ---------- 资源统计 ----------

type Stat struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsage   uint64  `json:"mem_usage"`
	MemLimit   uint64  `json:"mem_limit"`
	MemPercent float64 `json:"mem_percent"`
	NetRx      uint64  `json:"net_rx"`
	NetTx      uint64  `json:"net_tx"`
	BlkRead    uint64  `json:"blk_read"`
	BlkWrite   uint64  `json:"blk_write"`
	PIDs       uint64  `json:"pids"`

	// cpuOK 表示 CPU 增量这一帧是否有效。内存/网络/IO 是瞬时值，
	// 只要读到帧就是可信的，不受此字段影响。
	cpuOK bool
}

// CPUAvailable 供上层判断 CPU 百分比是否有意义。
func (s *Stat) CPUAvailable() bool { return s != nil && s.cpuOK }

// statsSample 是 Docker /stats 接口裁剪后的结构，避免跟随 SDK 结构体变化。
type statsSample struct {
	Name     string    `json:"name"`
	ID       string    `json:"id"`
	Read     time.Time `json:"read"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IoServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// hasStats 判断这一帧是否真的带了 cgroup 数据。
// Docker 对「不在运行」或统计失败的容器会返回只有 name/id 的空帧，
// 如果把它当成正常帧，界面就会一直显示 0（这正是之前的假象来源）。
func (r *statsSample) hasStats() bool {
	return r.MemoryStats.Limit > 0 ||
		r.MemoryStats.Usage > 0 ||
		r.CPUStats.CPUUsage.TotalUsage > 0 ||
		len(r.MemoryStats.Stats) > 0
}

const (
	// 单次采集上限。Docker 每秒出一帧，正常 2 秒内就能拿到可用样本
	statsTimeout = 8 * time.Second
	// 列表页并发采集的整体上限，避免容器很多时把接口拖住
	statsFillTimeout = 15 * time.Second
	// 最多读几帧，异常时不至于一直读流
	statsMaxFrames = 6
)

// StatOnce 采集一次实时资源占用。
func (s *Service) StatOnce(ctx context.Context, id string) (*Stat, error) {
	return s.collectStats(ctx, id)
}

// collectStats 采集一次实时资源占用。
// CPU 百分比必须是两帧的增量，单帧算不出来，所以走流式读两帧；内存等瞬时值读一帧即可。
func (s *Service) collectStats(ctx context.Context, id string) (*Stat, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, statsTimeout)
	defer cancel()

	resp, err := cli.ContainerStats(cctx, id, true)
	if err != nil {
		// 流式打不开时退化成单帧采样，至少把内存占用带回去
		if st, oerr := s.statsOnce(ctx, cli, id); oerr == nil && st != nil {
			return st, nil
		}
		return nil, err
	}
	defer resp.Body.Close()

	if strings.EqualFold(resp.OSType, "windows") {
		return nil, errors.New("Windows 容器暂不支持资源占用采集")
	}

	st, err := readStats(resp.Body)
	if err != nil {
		if o, oerr := s.statsOnce(ctx, cli, id); oerr == nil && o != nil {
			return o, nil
		}
		return nil, err
	}
	return st, nil
}

// statsOnce 用 one-shot 只取一帧。Docker 不会带上一帧，所以 CPU 增量算不出来，
// 但内存 / 网络 / IO 都是真实值，用于流式失败时兜底。
func (s *Service) statsOnce(ctx context.Context, cli *client.Client, id string) (*Stat, error) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	resp, err := cli.ContainerStatsOneShot(cctx, id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if strings.EqualFold(resp.OSType, "windows") {
		return nil, errors.New("Windows 容器暂不支持资源占用采集")
	}
	var raw statsSample
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("读取容器资源占用失败: %w", err)
	}
	if !raw.hasStats() {
		return nil, errors.New("容器未在运行或无法读取 cgroup 统计")
	}
	return statFromSample(raw), nil
}

// readStats 从 stats 流里读取足够算 CPU 的样本。优先用 precpu_stats（Docker 已缓存上一帧），
// 缺失时再读下一帧自己算差值。全程跳过空帧。
func readStats(r io.Reader) (*Stat, error) {
	dec := json.NewDecoder(r)
	var (
		last    *Stat
		prev    *[2]uint64
		gotData bool
	)
	for i := 0; i < statsMaxFrames; i++ {
		var raw statsSample
		if err := dec.Decode(&raw); err != nil {
			if last == nil {
				return nil, fmt.Errorf("读取容器资源占用失败: %w", err)
			}
			break
		}
		if !raw.hasStats() {
			continue
		}
		gotData = true
		st := statFromSample(raw)
		cur := [2]uint64{raw.CPUStats.CPUUsage.TotalUsage, raw.CPUStats.SystemUsage}
		base := prev
		if base == nil && raw.PreCPUStats.SystemUsage > 0 {
			base = &[2]uint64{raw.PreCPUStats.CPUUsage.TotalUsage, raw.PreCPUStats.SystemUsage}
		}
		// 系统 CPU 时间必须递增，否则说明这一帧和上一帧是同一时刻，增量没有意义
		if base != nil && cur[1] > base[1] {
			st.CPUPercent = calcCPUPercent(cur[0], cur[1], base[0], base[1], raw.CPUStats.OnlineCPUs)
			st.cpuOK = true
			return st, nil
		}
		last = st
		prev = &cur
	}
	if !gotData {
		return nil, errors.New("容器未在运行或无法读取 cgroup 统计")
	}
	if last == nil {
		return nil, errors.New("没有读取到容器资源占用数据")
	}
	// 只拿到一帧：内存等瞬时值可用，CPU 增量缺失
	return last, nil
}

func statFromSample(raw statsSample) *Stat {
	st := &Stat{
		MemUsage: raw.MemoryStats.Usage,
		MemLimit: raw.MemoryStats.Limit,
		PIDs:     raw.PidsStats.Current,
	}
	// cgroup v1 / v2 的字段名不同，两个都试；不扣掉 cache 会把内存占用算高
	if v, ok := raw.MemoryStats.Stats["inactive_file"]; ok && st.MemUsage > v {
		st.MemUsage -= v
	} else if v, ok := raw.MemoryStats.Stats["total_inactive_file"]; ok && st.MemUsage > v {
		st.MemUsage -= v
	}
	if st.MemLimit > 0 {
		st.MemPercent = round2(float64(st.MemUsage) / float64(st.MemLimit) * 100)
	}
	for _, n := range raw.Networks {
		st.NetRx += n.RxBytes
		st.NetTx += n.TxBytes
	}
	for _, b := range raw.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(b.Op) {
		case "read":
			st.BlkRead += b.Value
		case "write":
			st.BlkWrite += b.Value
		}
	}
	return st
}

// calcCPUPercent 用相邻两帧的 CPU 时间差除以系统时间差，再乘以核数换算成占比。
func calcCPUPercent(curTotal, curSys, preTotal, preSys uint64, cpus uint32) float64 {
	if curSys <= preSys || curTotal < preTotal {
		return 0
	}
	cpuDelta := float64(curTotal) - float64(preTotal)
	sysDelta := float64(curSys) - float64(preSys)
	n := float64(cpus)
	if n < 1 {
		n = float64(runtime.NumCPU())
	}
	if n < 1 {
		n = 1
	}
	return round2(cpuDelta / sysDelta * n * 100)
}

// FillStats 并发补充运行中容器的 CPU / 内存占用。单个容器采集失败只记录原因，
// 不影响整个列表返回，也不会把「未知」伪装成 0。
func (s *Service) FillStats(ctx context.Context, items []ContainerItem) {
	const workers = 8
	fctx, cancel := context.WithTimeout(ctx, statsFillTimeout)
	defer cancel()
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range items {
		if items[i].State != "running" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			st, err := s.collectStats(fctx, items[i].ID)
			if err != nil || st == nil {
				if err != nil {
					items[i].StatsError = err.Error()
				}
				return
			}
			items[i].CPUPercent = st.CPUPercent
			items[i].MemUsage = st.MemUsage
			items[i].MemLimit = st.MemLimit
			items[i].MemPercent = st.MemPercent
		}(i)
	}
	wg.Wait()
}

// ContainerStat 是单个容器的资源占用快照，供列表页在渲染完成后异步补齐。
type ContainerStat struct {
	ID         string  `json:"id"`
	CPUPercent float64 `json:"cpu_percent"`
	MemUsage   uint64  `json:"mem_usage"`
	MemLimit   uint64  `json:"mem_limit"`
	MemPercent float64 `json:"mem_percent"`
	StatsError string  `json:"stats_error"`
}

// StatsFor 批量采集资源占用。ids 为空时采集当前全部运行中的容器。
// 采集一次要等两帧（约 1 秒），所以列表页应当先渲染容器、再单独调用它。
func (s *Service) StatsFor(ctx context.Context, ids []string) ([]ContainerStat, error) {
	var items []ContainerItem
	if len(ids) == 0 {
		list, err := s.List(ctx, true, "")
		if err != nil {
			return nil, err
		}
		items = list
	} else {
		items = make([]ContainerItem, 0, len(ids))
		for _, id := range ids {
			if id = strings.TrimSpace(id); id == "" {
				continue
			}
			items = append(items, ContainerItem{ID: id, State: "running"})
		}
	}
	s.FillStats(ctx, items)
	out := make([]ContainerStat, 0, len(items))
	for _, it := range items {
		if it.State != "running" {
			continue
		}
		out = append(out, ContainerStat{
			ID:         it.ID,
			CPUPercent: it.CPUPercent,
			MemUsage:   it.MemUsage,
			MemLimit:   it.MemLimit,
			MemPercent: it.MemPercent,
			StatsError: it.StatsError,
		})
	}
	return out, nil
}

// ---------- 日志 ----------

type LogOptions struct {
	Tail       string
	Follow     bool
	Timestamps bool
	Since      string
	Until      string
}

func (s *Service) Logs(ctx context.Context, id string, opt LogOptions) (io.ReadCloser, bool, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, false, err
	}
	tail := opt.Tail
	if tail == "" {
		tail = "200"
	}
	rc, err := cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true, ShowStderr: true,
		Follow: opt.Follow, Tail: tail, Timestamps: opt.Timestamps,
		Since: opt.Since, Until: opt.Until,
	})
	if err != nil {
		return nil, false, err
	}
	info, err := cli.ContainerInspect(ctx, id)
	tty := false
	if err == nil {
		tty = info.Config != nil && info.Config.Tty
	}
	return rc, tty, nil
}

// ---------- 交互终端 ----------

type ExecSession struct {
	ID     string
	Conn   net.Conn
	Reader io.Reader
	Resize func(ctx context.Context, h, w uint) error
	Close  func()
}

func (s *Service) Exec(ctx context.Context, id string, cols, rows uint, shell string) (*ExecSession, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, err
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	created, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{shell},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Env:          []string{"TERM=xterm-256color"},
	})
	if err != nil {
		return nil, err
	}
	attach, err := cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return nil, err
	}
	if cols > 0 && rows > 0 {
		_ = cli.ContainerExecResize(ctx, created.ID, container.ResizeOptions{Height: rows, Width: cols})
	}
	sess := &ExecSession{
		ID:     created.ID,
		Conn:   attach.Conn,
		Reader: attach.Reader,
		Resize: func(ctx context.Context, h, w uint) error {
			return cli.ContainerExecResize(ctx, created.ID, container.ResizeOptions{Height: h, Width: w})
		},
		Close: func() { attach.Close() },
	}
	return sess, nil
}

func ptr[T any](v T) *T { return &v }

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
