//go:build windows

package system

// StartLocalShell 在 Windows 上不可用：creack/pty 仅支持类 Unix 系统。
// 服务端的目标运行环境是 Linux，此处返回明确提示而非静默失败。
func StartLocalShell(shell string) (Term, error) {
	return nil, ErrTermUnsupported
}
