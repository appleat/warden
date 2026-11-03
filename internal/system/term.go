package system

import "io"

// Term 是终端会话的统一抽象：本地 PTY 与远程 SSH 共用。
type Term interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

// ShellOption 描述本机一个可选的交互式 shell，供前端下拉选择。
// ID 就是 /ws/terminal/local?shell= 要传的值（shell 名字，或绝对路径）。
type ShellOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}
