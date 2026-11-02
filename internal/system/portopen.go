package system

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// AutoOpen 是「启动时自动放行监听端口」的执行结果，会随防火墙状态一起返回给界面。
type AutoOpen struct {
	Addr    string    `json:"addr"`
	Port    int       `json:"port"`
	Proto   string    `json:"proto"`
	Driver  string    `json:"driver"`
	Status  string    `json:"status"` // opened | exists | skipped | failed
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// ListenPort 从监听地址里取端口，支持 ":8080"、"0.0.0.0:8080"、"[::]:8080"。
func ListenPort(addr string) (int, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0, errors.New("监听地址为空")
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("监听地址 %q 解析失败", addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("监听地址 %q 的端口不合法", addr)
	}
	return port, nil
}

// IsLoopbackAddr 判断监听地址是否只绑定了本地回环。
// 这种情况外部本来就访问不到，放行防火墙没有意义，直接跳过。
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if host == "" {
		// 空 host 等价于 0.0.0.0，是监听所有网卡
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// inContainer 判断是否跑在容器里。
// 容器内的 ufw/firewalld 基本不存在，iptables 改的是容器自己的 netns，
// 对宿主机没用，硬改只会让人误以为已经放行。
func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "kubepods") || strings.Contains(s, "containerd")
}

// EnsurePortOpen 在启动时按监听地址自动放行防火墙端口。
//
// 只做「加一条 allow 规则」这一件事：不启停防火墙、不改动任何已有规则、可重复执行。
// 任何一步不满足（地址解析不了、只监听回环、容器内、没有防火墙、权限不足）都返回
// skipped/failed 而不是报错，启动流程不应该因为放行端口失败而中断。
func (f *Firewall) EnsurePortOpen(ctx context.Context, addr, comment string) *AutoOpen {
	res := &AutoOpen{Addr: addr, Proto: "tcp", At: time.Now()}

	f.mu.Lock()
	defer f.mu.Unlock()
	// 无论结果如何都记下来，界面上能看到「这次启动到底放行了没有、为什么没有」
	defer func() { f.autoOpen = res }()

	port, err := ListenPort(addr)
	if err != nil {
		res.Status, res.Message = "skipped", err.Error()
		return res
	}
	res.Port = port

	if IsLoopbackAddr(addr) {
		res.Status, res.Message = "skipped", "只监听本地回环，无需放行"
		return res
	}
	if inContainer() {
		res.Status, res.Message = "skipped", "检测到容器环境，请在宿主机放行该端口"
		return res
	}

	driver := f.detect()
	res.Driver = driver
	if driver == "none" {
		res.Status, res.Message = "skipped", "未检测到 ufw / firewalld / iptables"
		return res
	}
	if !isRoot() {
		res.Status, res.Message = "failed", "需要 root 权限才能修改防火墙规则"
		return res
	}

	switch driver {
	case "ufw":
		f.ufwEnsurePortLocked(ctx, res, port, comment)
	case "firewalld":
		f.firewalldEnsurePortLocked(ctx, res, port)
	case "iptables":
		f.iptablesEnsurePortLocked(ctx, res, port)
	}
	return res
}

// ---------- ufw ----------

// ufwPortAllowed 判断是否已有「放行该端口、来源任意」的规则。
func ufwPortAllowed(rules []Rule, port int) bool {
	want := strconv.Itoa(port)
	for _, r := range rules {
		if r.Action != "allow" || r.Port != want {
			continue
		}
		if r.Proto != "tcp" && r.Proto != "any" {
			continue
		}
		if r.From == "Anywhere" || r.From == "Anywhere (v6)" {
			return true
		}
	}
	return false
}

func (f *Firewall) ufwEnsurePortLocked(ctx context.Context, res *AutoOpen, port int, comment string) {
	if rules, err := f.ufwListLocked(ctx); err == nil && ufwPortAllowed(rules, port) {
		res.Status, res.Message = "exists", "已有放行规则"
		return
	}
	args := []string{"allow", fmt.Sprintf("%d/tcp", port)}
	if comment != "" {
		args = append(args, "comment", comment)
	}
	if _, err := runCmd(ctx, "ufw", args...); err != nil {
		res.Status, res.Message = "failed", err.Error()
		return
	}
	res.Status = "opened"
	if out, err := runCmd(ctx, "ufw", "status"); err == nil && !strings.Contains(out, "Status: active") {
		res.Message = "已添加规则，但 ufw 当前未启用，启用后生效"
		return
	}
	res.Message = "已添加放行规则"
}

// ---------- firewalld ----------

func (f *Firewall) firewalldEnsurePortLocked(ctx context.Context, res *AutoOpen, port int) {
	spec := fmt.Sprintf("%d/tcp", port)
	_, permErr := runCmd(ctx, "firewall-cmd", "--permanent", "--query-port="+spec)
	_, runErr := runCmd(ctx, "firewall-cmd", "--query-port="+spec)
	if permErr == nil && runErr == nil {
		res.Status, res.Message = "exists", "已有放行规则"
		return
	}

	var firstErr error
	opened, saved := false, false
	if runErr != nil {
		if _, err := runCmd(ctx, "firewall-cmd", "--add-port="+spec); err != nil {
			firstErr = err
		} else {
			opened = true
		}
	}
	if permErr != nil {
		if _, err := runCmd(ctx, "firewall-cmd", "--permanent", "--add-port="+spec); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			saved = true
		}
	}

	// 刻意不执行 --reload：reload 会把别人运行时加的规则一起冲掉，
	// 运行时规则 + 永久规则各加一次，效果等价且没有副作用。
	switch {
	case opened && saved:
		res.Status, res.Message = "opened", "已添加放行规则（运行时 + 永久）"
	case opened:
		res.Status, res.Message = "opened", "已添加运行时规则，永久配置写入失败，重启后会丢失"
	case saved:
		res.Status, res.Message = "opened", "已写入永久配置，firewalld 启动后生效"
	default:
		res.Status = "failed"
		res.Message = errText(firstErr, "firewall-cmd 执行失败")
	}
}

// ---------- iptables ----------

func (f *Firewall) iptablesEnsurePortLocked(ctx context.Context, res *AutoOpen, port int) {
	dport := strconv.Itoa(port)
	// -C 只做检查，不会改动规则
	if _, err := runCmd(ctx, "iptables", "-C", "INPUT", "-p", "tcp", "--dport", dport, "-j", "ACCEPT"); err == nil {
		res.Status, res.Message = "exists", "已有放行规则"
		return
	}
	if _, err := runCmd(ctx, "iptables", "-I", "INPUT", "-p", "tcp", "--dport", dport, "-j", "ACCEPT"); err != nil {
		res.Status, res.Message = "failed", err.Error()
		return
	}
	res.Status = "opened"
	res.Message = "已插入放行规则，iptables 规则重启后丢失，建议安装 iptables-persistent"
}

func errText(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	return err.Error()
}
