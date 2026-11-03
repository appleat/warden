//go:build !windows

package system

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creack/pty"
)

type localPTY struct {
	f   *os.File
	cmd *exec.Cmd
}

// unixShellCandidates 是常见 shell 的展示名，用于「本机终端」的下拉选择。
var unixShellCandidates = []struct{ id, name string }{
	{"bash", "Bash"},
	{"zsh", "Zsh"},
	{"fish", "Fish"},
	{"sh", "Sh"},
	{"dash", "Dash"},
	{"ksh", "Ksh"},
}

// LocalShells 列出本机可用的 shell：先按常见名字在 PATH 里找，再补 /etc/shells 里登记的。
func LocalShells() []ShellOption {
	out := make([]ShellOption, 0, len(unixShellCandidates))
	seen := map[string]bool{}
	for _, c := range unixShellCandidates {
		p, err := exec.LookPath(c.id)
		if err != nil || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, ShellOption{ID: c.id, Name: c.name, Path: p})
	}
	// /etc/shells 里登记的额外 shell（如 nu、elvish）也一并列出
	if b, err := os.ReadFile("/etc/shells"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || seen[line] {
				continue
			}
			if fi, err := os.Stat(line); err != nil || fi.IsDir() {
				continue
			}
			seen[line] = true
			out = append(out, ShellOption{ID: filepath.Base(line), Name: filepath.Base(line), Path: line})
		}
	}
	return out
}

// DefaultShellID 返回默认选中的 shell：优先用户登录 shell，其次 bash，最后 sh。
func DefaultShellID() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		if fi, err := os.Stat(sh); err == nil && !fi.IsDir() {
			return filepath.Base(sh)
		}
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash"
	}
	return "sh"
}

// resolveUnixShell 兼容两种写法：shell 名字（bash）与绝对路径（/usr/bin/zsh）。
// 都找不到时退回 sh，保证「打开终端」这件事本身不会失败。
func resolveUnixShell(shell string) string {
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell != "" {
		if !strings.ContainsRune(shell, os.PathSeparator) {
			if p, err := exec.LookPath(shell); err == nil {
				return p
			}
		} else if fi, err := os.Stat(shell); err == nil && !fi.IsDir() {
			return shell
		}
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "/bin/sh"
}

// StartLocalShell 启动本机交互式 shell。
func StartLocalShell(shell string) (Term, error) {
	cmd := exec.Command(resolveUnixShell(shell), "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=C.UTF-8")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100})
	if err != nil {
		return nil, err
	}
	return &localPTY{f: f, cmd: cmd}, nil
}

func (p *localPTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *localPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *localPTY) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	return pty.Setsize(p.f, &pty.Winsize{Rows: rows, Cols: cols})
}

func (p *localPTY) Close() error {
	err := p.f.Close()
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	}
	return err
}
