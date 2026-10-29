//go:build !windows

package system

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

type localPTY struct {
	f   *os.File
	cmd *exec.Cmd
}

// StartLocalShell 启动本机交互式 shell。
func StartLocalShell(shell string) (Term, error) {
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/bash"
	}
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-l")
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
