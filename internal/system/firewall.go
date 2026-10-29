package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rule 是统一的防火墙规则表示。
type Rule struct {
	ID      string `json:"id"`
	Action  string `json:"action"` // allow | deny
	Port    string `json:"port"`   // 80 / 8000:9000
	Proto   string `json:"proto"`  // tcp | udp | any
	From    string `json:"from"`   // 来源 IP/CIDR，空表示 Anywhere
	Comment string `json:"comment"`
	Raw     string `json:"raw"`
	IPv6    bool   `json:"ipv6"`
}

type FirewallStatus struct {
	Driver    string `json:"driver"`
	Enabled   bool   `json:"enabled"`
	Rules     []Rule `json:"rules"`
	Supported bool   `json:"supported"`
	Message   string `json:"message"`
	CanEdit   bool   `json:"can_edit"`
}

// Firewall 负责探测并操作本机防火墙。
type Firewall struct {
	mu     sync.Mutex
	driver string
	guard  *revertGuard
}

func NewFirewall() *Firewall {
	return &Firewall{guard: newRevertGuard()}
}

func (f *Firewall) detect() string {
	if f.driver != "" {
		return f.driver
	}
	switch {
	case hasCmd("ufw"):
		f.driver = "ufw"
	case hasCmd("firewall-cmd"):
		f.driver = "firewalld"
	case hasCmd("iptables"):
		f.driver = "iptables"
	default:
		f.driver = "none"
	}
	return f.driver
}

func hasCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	combined := out.String()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return combined, errors.New(msg)
	}
	return combined, nil
}

func (f *Firewall) Status(ctx context.Context) *FirewallStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.detect()
	st := &FirewallStatus{Driver: d, Rules: []Rule{}, CanEdit: isRoot()}
	switch d {
	case "ufw":
		st.Supported = true
		out, err := run(ctx, "ufw", "status")
		if err != nil {
			st.Message = err.Error()
			return st
		}
		st.Enabled = strings.Contains(out, "Status: active")
		rules, err := f.ufwListLocked(ctx)
		if err != nil {
			st.Message = err.Error()
		}
		st.Rules = rules
	case "firewalld":
		st.Supported = true
		out, err := run(ctx, "firewall-cmd", "--state")
		if err != nil {
			st.Message = err.Error()
			return st
		}
		st.Enabled = strings.TrimSpace(out) == "running"
		if st.Enabled {
			rules, err := f.firewalldListLocked(ctx)
			if err != nil {
				st.Message = err.Error()
			}
			st.Rules = rules
		}
	case "iptables":
		st.Supported = true
		st.Enabled = true
		rules, err := f.iptablesListLocked(ctx)
		if err != nil {
			st.Message = err.Error()
		}
		st.Rules = rules
		st.Message = "iptables 规则重启后会丢失，如需持久化请安装 iptables-persistent"
	default:
		st.Supported = false
		st.Message = "未检测到 ufw / firewalld / iptables"
	}
	return st
}

// ---------- ufw ----------

var ufwLine = regexp.MustCompile(`^\[\s*(\d+)\]\s+(.+?)\s{2,}(ALLOW|DENY|REJECT|LIMIT)(?:\s+(IN|OUT|FWD))?\s+(.+)$`)

func (f *Firewall) ufwListLocked(ctx context.Context) ([]Rule, error) {
	out, err := run(ctx, "ufw", "status", "numbered")
	if err != nil {
		return nil, err
	}
	rules := []Rule{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		m := ufwLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		raw := strings.TrimSpace(line)
		rules = append(rules, Rule{
			ID:     m[1],
			Action: strings.ToLower(m[3]),
			Port:   ufwExtractPort(m[2]),
			Proto:  ufwExtractProto(m[2]),
			From:   strings.TrimSpace(m[5]),
			Raw:    raw,
			IPv6:   strings.Contains(m[5], "(v6)"),
		})
	}
	return rules, nil
}

func ufwExtractPort(to string) string {
	s := strings.TrimSpace(to)
	s = strings.TrimSuffix(s, "(v6)")
	// 形如 "22/tcp" 或 "3306" 或 "Anywhere on eth0"
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	p := fields[0]
	if strings.Contains(p, "/") {
		p = strings.SplitN(p, "/", 2)[0]
	}
	if p == "Anywhere" || p == "Anywhere6" {
		return ""
	}
	return p
}

func ufwExtractProto(to string) string {
	if i := strings.LastIndex(to, "/"); i >= 0 && i+1 < len(to) {
		p := strings.TrimSuffix(to[i+1:], "(v6)")
		p = strings.TrimSpace(p)
		if p == "tcp" || p == "udp" {
			return p
		}
	}
	return "any"
}

func (f *Firewall) ufwAddLocked(ctx context.Context, r Rule) error {
	args := []string{r.Action}
	if r.From != "" && r.From != "Anywhere" && r.From != "Anywhere (v6)" {
		args = append(args, "from", r.From)
	}
	if r.Port != "" {
		if r.From != "" && r.From != "Anywhere" {
			args = append(args, "to", "any", "port", r.Port)
		} else {
			args = append(args, r.Port)
		}
		if r.Proto != "" && r.Proto != "any" {
			args = append(args, "proto", r.Proto)
		}
	}
	if r.Comment != "" {
		args = append(args, "comment", r.Comment)
	}
	_, err := run(ctx, "ufw", args...)
	return err
}

func (f *Firewall) ufwDeleteLocked(ctx context.Context, id string) error {
	_, err := run(ctx, "ufw", "--force", "delete", id)
	return err
}

func (f *Firewall) ufwSetEnabledLocked(ctx context.Context, on bool) error {
	arg := "enable"
	if !on {
		arg = "disable"
	}
	_, err := run(ctx, "ufw", "--force", arg)
	return err
}

func (f *Firewall) ufwSnapshotLocked(ctx context.Context) ([]Rule, error) {
	return f.ufwListLocked(ctx)
}

func (f *Firewall) ufwRestoreLocked(ctx context.Context, rules []Rule) error {
	if _, err := run(ctx, "ufw", "--force", "reset"); err != nil {
		return err
	}
	for _, r := range rules {
		if err := f.ufwAddLocked(ctx, r); err != nil {
			return err
		}
	}
	_, err := run(ctx, "ufw", "--force", "enable")
	return err
}

// ---------- firewalld ----------

func (f *Firewall) firewalldListLocked(ctx context.Context) ([]Rule, error) {
	out, err := run(ctx, "firewall-cmd", "--list-ports")
	if err != nil {
		return nil, err
	}
	rules := []Rule{}
	for i, item := range strings.Fields(out) {
		parts := strings.SplitN(item, "/", 2)
		proto := "tcp"
		if len(parts) == 2 {
			proto = parts[1]
		}
		rules = append(rules, Rule{
			ID:     strconv.Itoa(i + 1),
			Action: "allow",
			Port:   parts[0],
			Proto:  proto,
			From:   "Anywhere",
			Raw:    item,
		})
	}
	return rules, nil
}

func (f *Firewall) firewalldAddLocked(ctx context.Context, r Rule) error {
	if r.Port == "" {
		return errors.New("firewalld 需要指定端口")
	}
	proto := r.Proto
	if proto == "" || proto == "any" {
		proto = "tcp"
	}
	spec := fmt.Sprintf("%s/%s", r.Port, proto)
	if _, err := run(ctx, "firewall-cmd", "--permanent", "--add-port="+spec); err != nil {
		return err
	}
	_, err := run(ctx, "firewall-cmd", "--reload")
	return err
}

func (f *Firewall) firewalldDeleteLocked(ctx context.Context, id string) error {
	rules, err := f.firewalldListLocked(ctx)
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(id)
	if n < 1 || n > len(rules) {
		return errors.New("规则不存在")
	}
	r := rules[n-1]
	if _, err := run(ctx, "firewall-cmd", "--permanent", "--remove-port="+r.Raw); err != nil {
		return err
	}
	_, err = run(ctx, "firewall-cmd", "--reload")
	return err
}

func (f *Firewall) firewalldSetEnabledLocked(ctx context.Context, on bool) error {
	arg := "--reload"
	if !on {
		arg = "--panic-on"
		if _, err := run(ctx, "firewall-cmd", arg); err != nil {
			return err
		}
		return nil
	}
	_, err := run(ctx, "firewall-cmd", "--panic-off")
	return err
}

// ---------- iptables ----------

func (f *Firewall) iptablesListLocked(ctx context.Context) ([]Rule, error) {
	out, err := run(ctx, "iptables", "-S", "INPUT")
	if err != nil {
		return nil, err
	}
	rules := []Rule{}
	idx := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-A INPUT") {
			continue
		}
		idx++
		r := Rule{ID: strconv.Itoa(idx), Raw: line, From: "Anywhere", Proto: "any"}
		if strings.Contains(line, "-j ACCEPT") {
			r.Action = "allow"
		} else if strings.Contains(line, "-j DROP") || strings.Contains(line, "-j REJECT") {
			r.Action = "deny"
		} else {
			r.Action = "other"
		}
		if m := regexp.MustCompile(`--dport (\d+)`).FindStringSubmatch(line); m != nil {
			r.Port = m[1]
		}
		if m := regexp.MustCompile(`-p (\w+)`).FindStringSubmatch(line); m != nil {
			r.Proto = m[1]
		}
		if m := regexp.MustCompile(`-s ([\d./]+)`).FindStringSubmatch(line); m != nil {
			r.From = m[1]
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func (f *Firewall) iptablesAddLocked(ctx context.Context, r Rule) error {
	args := []string{"-I", "INPUT"}
	if r.Proto != "" && r.Proto != "any" {
		args = append(args, "-p", r.Proto)
	}
	if r.From != "" && r.From != "Anywhere" {
		args = append(args, "-s", r.From)
	}
	if r.Port != "" {
		args = append(args, "--dport", r.Port)
	}
	if r.Action == "deny" {
		args = append(args, "-j", "DROP")
	} else {
		args = append(args, "-j", "ACCEPT")
	}
	_, err := run(ctx, "iptables", args...)
	return err
}

func (f *Firewall) iptablesDeleteLocked(ctx context.Context, id string) error {
	rules, err := f.iptablesListLocked(ctx)
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(id)
	if n < 1 || n > len(rules) {
		return errors.New("规则不存在")
	}
	spec := strings.Fields(strings.TrimPrefix(rules[n-1].Raw, "-A INPUT"))
	_, err = run(ctx, "iptables", append([]string{"-D", "INPUT"}, spec...)...)
	return err
}

// ---------- 对外操作 ----------

func (f *Firewall) AddRule(ctx context.Context, r Rule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := validateRule(r); err != nil {
		return err
	}
	switch f.detect() {
	case "ufw":
		return f.ufwAddLocked(ctx, r)
	case "firewalld":
		return f.firewalldAddLocked(ctx, r)
	case "iptables":
		return f.iptablesAddLocked(ctx, r)
	}
	return errors.New("没有可用的防火墙")
}

func (f *Firewall) DeleteRule(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reNumber.MatchString(id) {
		return errors.New("规则编号不合法")
	}
	switch f.detect() {
	case "ufw":
		return f.ufwDeleteLocked(ctx, id)
	case "firewalld":
		return f.firewalldDeleteLocked(ctx, id)
	case "iptables":
		return f.iptablesDeleteLocked(ctx, id)
	}
	return errors.New("没有可用的防火墙")
}

func (f *Firewall) SetEnabled(ctx context.Context, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.detect() {
	case "ufw":
		return f.ufwSetEnabledLocked(ctx, on)
	case "firewalld":
		return f.firewalldSetEnabledLocked(ctx, on)
	}
	return errors.New("当前防火墙不支持开关操作")
}

var (
	rePort    = regexp.MustCompile(`^\d{1,5}(:\d{1,5})?$`)
	reFrom    = regexp.MustCompile(`^[\d.:a-fA-F/]+$`)
	reComment = regexp.MustCompile(`^[\w\p{Han} ._-]{0,60}$`)
	reNumber  = regexp.MustCompile(`^\d+$`)
)

func validateRule(r Rule) error {
	if r.Action != "allow" && r.Action != "deny" {
		return errors.New("动作只能是 allow 或 deny")
	}
	if r.Port != "" {
		if !rePort.MatchString(r.Port) {
			return errors.New("端口格式不正确，示例：80 或 8000:9000")
		}
		for _, seg := range strings.Split(r.Port, ":") {
			n, err := strconv.Atoi(seg)
			if err != nil || n < 1 || n > 65535 {
				return errors.New("端口必须在 1-65535 之间")
			}
		}
	}
	if r.Proto != "" && r.Proto != "any" && r.Proto != "tcp" && r.Proto != "udp" {
		return errors.New("协议只能是 tcp / udp / any")
	}
	if r.From != "" && !reFrom.MatchString(r.From) {
		return errors.New("来源地址格式不正确")
	}
	if r.Comment != "" && !reComment.MatchString(r.Comment) {
		return errors.New("备注包含非法字符")
	}
	return nil
}

// ---------- 危险操作自动回滚 ----------

type revertGuard struct {
	mu      sync.Mutex
	timer   *time.Timer
	snap    []Rule
	pending string
}

func newRevertGuard() *revertGuard { return &revertGuard{} }

// Schedule 在 seconds 秒后回滚到 snap，除非调用 Confirm。
func (g *revertGuard) Schedule(seconds int, snap []Rule, restore func([]Rule) error) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
	}
	token := strconv.FormatInt(time.Now().UnixNano(), 36)
	g.snap = snap
	g.pending = token
	g.timer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.pending != token {
			return
		}
		_ = restore(g.snap)
		g.pending = ""
	})
	return token
}

func (g *revertGuard) Confirm(token string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == "" || g.pending != token {
		return false
	}
	if g.timer != nil {
		g.timer.Stop()
	}
	g.pending = ""
	return true
}

func (g *revertGuard) Pending() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pending != ""
}

// ---------- 快照与回滚（对外） ----------

// Snapshot 记录当前规则，仅 ufw 支持完整恢复。
func (f *Firewall) Snapshot(ctx context.Context) ([]Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detect() != "ufw" {
		return nil, errors.New("当前防火墙不支持自动回滚")
	}
	return f.ufwSnapshotLocked(ctx)
}

// ScheduleRevert 在 seconds 秒后自动恢复到 rules。
func (f *Firewall) ScheduleRevert(seconds int, rules []Rule) string {
	return f.guard.Schedule(seconds, rules, func(snap []Rule) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return f.ufwRestoreLocked(ctx, snap)
	})
}

func (f *Firewall) ConfirmRevert(token string) bool { return f.guard.Confirm(token) }

func (f *Firewall) HasPendingRevert() bool { return f.guard.Pending() }
