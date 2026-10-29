package metrics

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
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

type Collector struct {
	mu       sync.RWMutex
	snapshot Snapshot
	history  []Sample
	initDone bool

	lastRx, lastTx uint64
	lastNetTime    time.Time
}

func New() *Collector {
	return &Collector{history: make([]Sample, 0, historyLen)}
}

// Start 后台每秒采样一次；context 取消即退出。
func (c *Collector) Start(ctx context.Context) {
	c.collect()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.collect()
			}
		}
	}()
}

func (c *Collector) collect() {
	snap := Snapshot{Time: time.Now().UnixMilli()}

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
	if io, err := net.IOCounters(false); err == nil && len(io) > 0 {
		rx, tx := io[0].BytesRecv, io[0].BytesSent
		snap.NetRxBytes, snap.NetTxBytes = rx, tx
		now := time.Now()
		if !c.lastNetTime.IsZero() {
			sec := now.Sub(c.lastNetTime).Seconds()
			if sec > 0 && rx >= c.lastRx {
				snap.NetRxRate = round2(float64(rx-c.lastRx) / sec)
				snap.NetTxRate = round2(float64(tx-c.lastTx) / sec)
			}
		}
		c.lastRx, c.lastTx, c.lastNetTime = rx, tx, now
	}
	snap.Disks = c.disks()
	snap.Host = c.hostInfo()

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

func (c *Collector) disks() []DiskInfo {
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

func (c *Collector) hostInfo() HostInfo {
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
	if vm, err := mem.VirtualMemory(); err == nil {
		h.TotalMem = vm.Total
	}
	return h
}

// Snapshot 返回当前快照（含最近曲线）。
func (c *Collector) Snapshot() (Snapshot, []Sample) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	hist := make([]Sample, len(c.history))
	copy(hist, c.history)
	return c.snapshot, hist
}

// TopProcesses 返回按 CPU 排序的前 n 个进程。
func (c *Collector) TopProcesses(n int) []ProcInfo {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	out := make([]ProcInfo, 0, len(procs))
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || name == "" {
			continue
		}
		pi := ProcInfo{PID: p.Pid, Name: name}
		if v, err := p.CPUPercent(); err == nil {
			pi.CPU = round2(v)
		}
		if v, err := p.MemoryPercent(); err == nil {
			pi.MemPct = round2(float64(v))
		}
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			pi.RSS = mi.RSS
		}
		if u, err := p.Username(); err == nil {
			pi.User = u
		}
		if cl, err := p.Cmdline(); err == nil {
			pi.Cmdline = truncate(cl, 160)
		}
		out = append(out, pi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPU > out[j].CPU })
	if len(out) > n {
		out = out[:n]
	}
	return out
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
