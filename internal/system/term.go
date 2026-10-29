package system

import (
	"errors"
	"io"
)

// Term 是终端会话的统一抽象：本地 PTY 与远程 SSH 共用。
type Term interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

var ErrTermUnsupported = errors.New("当前操作系统不支持本地 PTY 终端，请使用 SSH 终端")
