package metrics

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

type HostInfo struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`
	Uptime   uint64 `json:"uptime"`
	BootTime uint64 `json:"boot_time"`
	CPUCores int    `json:"cpu_cores"`
	CPUModel string `json:"cpu_model"`
	TotalMem uint64 `json:"total_mem"`
}

type DiskInfo struct {
	Device     string  `json:"device"`
	Mountpoint string  `json:"mountpoint"`
	FSType     string  `json:"fstype"`
	Total      uint64  `json:"total"`
	Used       uint64  `json:"used"`
	Free       uint64  `json:"free"`
	Percent    float64 `json:"percent"`
}

type Snapshot struct {
	Time        int64      `json:"time"`
	CPUPercent  float64    `json:"cpu_percent"`
	PerCore     []float64  `json:"per_core"`
	Load1       float64    `json:"load1"`
	Load5       float64    `json:"load5"`
	Load15      float64    `json:"load15"`
	MemTotal    uint64     `json:"mem_total"`
	MemUsed     uint64     `json:"mem_used"`
	MemPercent  float64    `json:"mem_percent"`
	SwapTotal   uint64     `json:"swap_total"`
	SwapUsed    uint64     `json:"swap_used"`
	SwapPercent float64    `json:"swap_percent"`
	NetRxBytes  uint64     `json:"net_rx_bytes"`
	NetTxBytes  uint64     `json:"net_tx_bytes"`
	NetRxRate   float64    `json:"net_rx_rate"`
	NetTxRate   float64    `json:"net_tx_rate"`
	Disks       []DiskInfo `json:"disks"`
	Host        HostInfo   `json:"host"`
}

type Sample struct {
	Time       int64   `json:"t"`
	CPU        float64 `json:"cpu"`
	Mem        float64 `json:"mem"`
	NetRx      float64 `json:"net_rx"`
	NetTx      float64 `json:"net_tx"`
	Load1      float64 `json:"load1"`
	DiskUsedPc float64 `json:"disk_pct"`
}

type ProcInfo struct {
	PID     int32   `json:"pid"`
	Name    string  `json:"name"`
	CPU     float64 `json:"cpu"`
	MemPct  float64 `json:"mem_pct"`
	RSS     uint64  `json:"rss"`
	User    string  `json:"user"`
	Cmdline string  `json:"cmdline"`
}

const historyLen = 180

// 采集节奏。
//
// 面板只有被打开时才会读快照，所以没人访问的时候没必要每秒全量采一遍：
// 空闲时退到 10s 一次，CPU 占用可以从「恒定 2% 单核」压到接近 0。
const (
	activeInterval = time.Second      // 有人在看：1s 一个采样点
	idleInterval   = 10 * time.Second // 没人看：10s 一个点
	activeWindow   = 45 * time.Second // 最后一次读取后多久算「没人看」

	// 下面几项要么是系统调用很贵、要么是数值几乎不变，统一降频，
	// 中间的 tick 复用上一次的结果，避免每秒重复付出代价。
	netEvery  = 2 * time.Second  // 网卡计数：Windows 上 GetAdaptersAddresses 单次约 20ms
	diskEvery = 15 * time.Second // 分区用量：变化以分钟计
	hostEvery = 10 * time.Minute // 主机静态信息：型号/内核/启动时间根本不会变
)

type Collector struct {
	mu       sync.RWMutex
	snapshot Snapshot
	history  []Sample
	initDone bool

	// 网络计数：只按 netEvery 采样，其余 tick 复用
	netRx, netTx         uint64
	netRateRx, netRateTx float64
	netAt                time.Time

	// 磁盘与主机信息缓存
	disks  []DiskInfo
	diskAt time.Time
	host   HostInfo
	hostAt time.Time

	lastCollect time.Time
	lastAccess  atomic.Int64 // 最近一次被读取的时间（UnixNano）
	wake        chan struct{}
}

func New() *Collector {
	return &Collector{
		history: make([]Sample, 0, historyLen),
		wake:    make(chan struct{}, 1),
	}
}

// Touch 标记「有人在看」，让后台采集切回秒级节奏，并立刻唤醒一次补采，
// 避免刚打开页面时拿到最多 10 秒前的旧数据。
func (c *Collector) Touch() {
	c.lastAccess.Store(time.Now().UnixNano())
	select {
	case c.wake <- struct{}{}:
	default: // 已有待处理的唤醒信号，不重复排队
	}
}

// Interval 返回距下一次采样应等待的时长：最近有人读过就 1s，否则 10s。
func (c *Collector) Interval() time.Duration {
	if last := c.lastAccess.Load(); last != 0 && time.Since(time.Unix(0, last)) < activeWindow {
		return activeInterval
	}
	return idleInterval
}

// Start 启动后台采样；context 取消即退出。
func (c *Collector) Start(ctx context.Context) {
	c.Touch()
	c.collect()
	go func() {
		// 用可重置的定时器而不是固定 ticker：空闲时每 10 秒才醒一次，
		// 有人来读则被 Touch 立刻唤醒，不必等到下一个周期。
		t := time.NewTimer(c.Interval())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.wake:
				// 读取方主动唤醒：只在快照确实过期时才补采，密集请求被限流到 1s 一次
				if time.Since(c.lastCollect) >= activeInterval {
					c.collect()
					t.Reset(c.Interval())
				}
			case <-t.C:
				c.collect()
				t.Reset(c.Interval())
			}
		}
	}()
}

func (c *Collector) collect() {
	now := time.Now()
	c.lastCollect = now
	snap := Snapshot{Time: now.UnixMilli()}
	// 空闲期只采最便宜的 CPU / 内存：网卡、磁盘、主机信息没有消费方，
	// 却是开销的大头（Windows 上网卡枚举单次就要 20ms）。有人打开页面后
	// 第一次唤醒就会把这几项补采回来。
	idle := c.Interval() == idleInterval

	// 只调用一次：gopsutil 的 Percent 基于「上次调用」计算差值，重复调用会得到 0
	if per, err := cpu.Percent(0, true); err == nil && len(per) > 0 {
		var sum float64
		snap.PerCore = make([]float64, len(per))
		for i, v := range per {
			sum += v
			snap.PerCore[i] = round2(v)
		}
		snap.CPUPercent = round2(sum / float64(len(per)))
	}
	if avg, err := load.Avg(); err == nil {
		snap.Load1, snap.Load5, snap.Load15 = round2(avg.Load1), round2(avg.Load5), round2(avg.Load15)
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		snap.MemTotal, snap.MemUsed, snap.MemPercent = vm.Total, vm.Used, round2(vm.UsedPercent)
	}
	if sw, err := mem.SwapMemory(); err == nil {
		snap.SwapTotal, snap.SwapUsed, snap.SwapPercent = sw.Total, sw.Used, round2(sw.UsedPercent)
	}

	// 网络：代价最高的一项，按 netEvery 采样，其余 tick 复用上次速率
	if !idle && (c.netAt.IsZero() || now.Sub(c.netAt) >= netEvery) {
		c.sampleNet(&snap, now)
	} else {
		snap.NetRxBytes, snap.NetTxBytes = c.netRx, c.netTx
		snap.NetRxRate, snap.NetTxRate = c.netRateRx, c.netRateTx
	}

	// 磁盘用量与主机静态信息同理，各自按自己的周期刷新
	if !idle && (c.diskAt.IsZero() || now.Sub(c.diskAt) >= diskEvery) {
		c.disks = c.readDisks()
		c.diskAt = now
	}
	snap.Disks = c.disks

	if c.hostAt.IsZero() || now.Sub(c.hostAt) >= hostEvery {
		c.host = c.readHost(snap.MemTotal)
		c.hostAt = now
	}
	snap.Host = c.host

	c.mu.Lock()
	c.snapshot = snap
	sample := Sample{Time: snap.Time, CPU: snap.CPUPercent, Mem: snap.MemPercent,
		NetRx: snap.NetRxRate, NetTx: snap.NetTxRate, Load1: snap.Load1, DiskUsedPc: maxDiskPct(snap.Disks)}
	c.history = append(c.history, sample)
	if len(c.history) > historyLen {
		c.history = c.history[len(c.history)-historyLen:]
	}
	c.initDone = true
	c.mu.Unlock()
}

// sampleNet 读取网卡计数并换算速率；间隔按实测 Δt 计算，不假定固定周期。
func (c *Collector) sampleNet(snap *Snapshot, now time.Time) {
	counters, err := net.IOCounters(true)
	if err != nil || len(counters) == 0 {
		// 读失败时保留上一次的数值，避免曲线突然掉到 0
		snap.NetRxBytes, snap.NetTxBytes = c.netRx, c.netTx
		snap.NetRxRate, snap.NetTxRate = c.netRateRx, c.netRateTx
		return
	}
	rx, tx := sumNet(counters)
	snap.NetRxBytes, snap.NetTxBytes = rx, tx
	if !c.netAt.IsZero() {
		if sec := now.Sub(c.netAt).Seconds(); sec > 0 && rx >= c.netRx && tx >= c.netTx {
			c.netRateRx = round2(float64(rx-c.netRx) / sec)
			c.netRateTx = round2(float64(tx-c.netTx) / sec)
		}
	}
	c.netRx, c.netTx, c.netAt = rx, tx, now
	snap.NetRxRate, snap.NetTxRate = c.netRateRx, c.netRateTx
}

// sumNet 汇总全部有效网卡的收发字节。
//
// 之前直接取 counters[0]，但 Windows 上第一块常常是「已断开的本地连接」，
// 结果面板网速长期贴着 0。回环网卡要排除，否则本机进程互相通信会把曲线撑起来。
func sumNet(counters []net.IOCountersStat) (rx, tx uint64) {
	for _, c := range counters {
		name := strings.ToLower(c.Name)
		if name == "lo" || strings.Contains(name, "loopback") {
			continue
		}
		rx += c.BytesRecv
		tx += c.BytesSent
	}
	return rx, tx
}

func (c *Collector) readDisks() []DiskInfo {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]DiskInfo, 0, len(parts))
	for _, p := range parts {
		// 过滤容器/虚拟文件系统噪音
		if strings.HasPrefix(p.Device, "/dev/loop") || p.Fstype == "squashfs" ||
			p.Fstype == "tmpfs" || p.Fstype == "devtmpfs" || p.Fstype == "overlay" {
			continue
		}
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		key := p.Mountpoint
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, DiskInfo{
			Device: p.Device, Mountpoint: p.Mountpoint, FSType: p.Fstype,
			Total: u.Total, Used: u.Used, Free: u.Free, Percent: round2(u.UsedPercent),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mountpoint < out[j].Mountpoint })
	return out
}

// readHost 读取几乎不变的主机静态信息。totalMem 直接用本次内存采样结果，
// 避免再调一次 mem.VirtualMemory()。
func (c *Collector) readHost(totalMem uint64) HostInfo {
	h := HostInfo{}
	if info, err := host.Info(); err == nil {
		h.Hostname, h.OS, h.Platform, h.Version = info.Hostname, info.OS, info.Platform, info.PlatformVersion
		h.Kernel, h.Arch, h.Uptime, h.BootTime = info.KernelVersion, info.KernelArch, info.Uptime, info.BootTime
	}
	if arr, err := cpu.Info(); err == nil && len(arr) > 0 {
		h.CPUModel = strings.TrimSpace(arr[0].ModelName)
	}
	if n, err := cpu.Counts(true); err == nil {
		h.CPUCores = n
	}
	h.TotalMem = totalMem
	return h
}

// Snapshot 返回当前快照（含最近曲线）。每次读取都会刷新「有人在看」标记，
// 让后台采集保持秒级节奏。
func (c *Collector) Snapshot() (Snapshot, []Sample) {
	c.Touch()
	c.mu.RLock()
	defer c.mu.RUnlock()
	hist := make([]Sample, len(c.history))
	copy(hist, c.history)
	return c.snapshot, hist
}

func maxDiskPct(d []DiskInfo) float64 {
	var m float64
	for _, v := range d {
		if v.Percent > m {
			m = v.Percent
		}
	}
	return m
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
