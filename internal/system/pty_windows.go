//go:build windows

package system

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 没有 unix 的 pty，等价物是伪控制台（ConPTY，Windows 10 1809+）。
// 走 ConPTY 而不是普通管道，PowerShell / CMD 才会认为自己挂在真终端上：
// 颜色、Tab 补全、方向键历史、vim、ssh 这些交互程序才能正常工作。

type localPTY struct {
	hpc windows.Handle // 伪控制台句柄

	in  *os.File // 宿主侧：写 → 键盘输入
	out *os.File // 宿主侧：读 ← 屏幕输出

	// 交给伪控制台的那两端。必须一直保持打开：CreatePseudoConsole 并不复制句柄，
	// 提前关掉 input 读端会让 shell 读到 EOF 立刻退出（表现为终端闪一下就没了）。
	conIn  *os.File
	conOut *os.File

	proc   windows.Handle
	exited chan struct{}

	closeOnce sync.Once
	tearOnce  sync.Once
}

// shellSpec 是一个具体的 shell 启动方式。
type shellSpec struct {
	id   string
	name string
	path string
	args []string
}

func system32Path(parts ...string) string {
	dir, err := windows.GetSystemDirectory()
	if err != nil || dir == "" {
		dir = filepath.Join(os.Getenv("SystemRoot"), "System32")
	}
	return filepath.Join(append([]string{dir}, parts...)...)
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// windowsShells 按「可用性 + 常用度」列出本机 shell。
// powershell.exe 与 cmd.exe 是所有 Windows 都自带的，pwsh.exe（PowerShell 7）需要单独安装。
func windowsShells() []shellSpec {
	out := make([]shellSpec, 0, 3)
	if p := system32Path("WindowsPowerShell", "v1.0", "powershell.exe"); fileExists(p) {
		out = append(out, shellSpec{"powershell", "PowerShell", p, []string{"-NoLogo"}})
	}
	if p, err := exec.LookPath("pwsh.exe"); err == nil {
		out = append(out, shellSpec{"pwsh", "PowerShell 7", p, []string{"-NoLogo"}})
	}
	if p := system32Path("cmd.exe"); fileExists(p) {
		// chcp 65001 让 CMD 里的原生程序按 UTF-8 输出，中文不再乱码；
		// >nul 避免在终端里多打一行「活动代码页: 65001」。
		out = append(out, shellSpec{"cmd", "命令提示符", p, []string{"/K", "chcp 65001>nul"}})
	}
	return out
}

// LocalShells 列出本机可用的 shell，供前端下拉选择。
func LocalShells() []ShellOption {
	specs := windowsShells()
	out := make([]ShellOption, 0, len(specs))
	for _, s := range specs {
		out = append(out, ShellOption{ID: s.id, Name: s.name, Path: s.path})
	}
	return out
}

// DefaultShellID 默认用 PowerShell：命令能力比 CMD 强，且所有 Windows 都有。
func DefaultShellID() string {
	for _, s := range windowsShells() {
		if s.id == "powershell" {
			return s.id
		}
	}
	return "cmd"
}

// resolveShell 把前端传来的值（shell 名字或绝对路径）解析成可启动的命令。
func resolveShell(name string) (shellSpec, error) {
	name = strings.TrimSpace(name)
	specs := windowsShells()
	if name == "" {
		for _, s := range specs {
			if s.id == DefaultShellID() {
				return s, nil
			}
		}
		if len(specs) > 0 {
			return specs[0], nil
		}
		return shellSpec{}, errors.New("本机未找到可用的 shell（powershell.exe / cmd.exe 都不存在）")
	}
	// 绝对路径：config.yaml 里可以直接写死某个 shell
	if filepath.IsAbs(name) {
		if !fileExists(name) {
			return shellSpec{}, fmt.Errorf("指定的 shell 不存在：%s", name)
		}
		return shellSpec{id: name, name: filepath.Base(name), path: name}, nil
	}
	for _, s := range specs {
		if strings.EqualFold(s.id, name) || strings.EqualFold(s.name, name) {
			return s, nil
		}
	}
	// 兜底：PATH 里的其它 shell（如 git-bash、nu）
	if p, err := exec.LookPath(name); err == nil {
		return shellSpec{id: name, name: filepath.Base(name), path: p}, nil
	}
	return shellSpec{}, fmt.Errorf("不支持的 shell：%s", name)
}

// StartLocalShell 用伪控制台启动本机交互式 shell。
func StartLocalShell(shell string) (Term, error) {
	spec, err := resolveShell(shell)
	if err != nil {
		return nil, err
	}
	return startConPTY(spec)
}

func startConPTY(spec shellSpec) (Term, error) {
	// inR/conIn 这一对：我们写 inW，伪控制台读 conIn
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("创建输入管道失败: %w", err)
	}
	// outR/conOut 这一对：伪控制台写 conOut，我们读 outR
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, fmt.Errorf("创建输出管道失败: %w", err)
	}
	// 任一步失败都要把四个句柄收干净，否则每点一次「打开终端」就漏两个句柄
	cleanup := func() {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
	}

	hpc, err := createPseudoConsole(120, 30, windows.Handle(inR.Fd()), windows.Handle(outW.Fd()))
	if err != nil {
		cleanup()
		return nil, err
	}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		cleanup()
		windows.ClosePseudoConsole(hpc)
		return nil, fmt.Errorf("初始化进程属性失败: %w", err)
	}
	defer attrs.Delete()
	if err := setPseudoConsoleAttr(attrs, hpc); err != nil {
		cleanup()
		windows.ClosePseudoConsole(hpc)
		return nil, err
	}

	si := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			// STARTF_USESTDHANDLES 不能省：少了它，子进程的标准句柄不会走伪控制台，
			// shell 起来打印完 banner 就读到 EOF 直接退出（退出码 0，很像「终端闪一下」）。
			Flags: windows.STARTF_USESTDHANDLES,
		},
		ProcThreadAttributeList: attrs.List(),
	}
	appName, err := windows.UTF16PtrFromString(spec.path)
	if err != nil {
		cleanup()
		windows.ClosePseudoConsole(hpc)
		return nil, err
	}
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{spec.path}, spec.args...)))
	if err != nil {
		cleanup()
		windows.ClosePseudoConsole(hpc)
		return nil, err
	}

	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		appName, cmdLine, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		nil, nil, &si.StartupInfo, &pi,
	)
	if err != nil {
		cleanup()
		windows.ClosePseudoConsole(hpc)
		return nil, fmt.Errorf("启动 %s 失败: %w", spec.name, err)
	}
	windows.CloseHandle(pi.Thread)

	p := &localPTY{
		hpc:    hpc,
		in:     inW,
		out:    outR,
		conIn:  inR,
		conOut: outW,
		proc:   pi.Process,
		exited: make(chan struct{}),
	}
	// 子进程退出（例如输入 exit）后主动拆掉控制台：否则宿主侧的 Read 会一直阻塞，
	// 浏览器里就停在「看着像死机、其实 shell 已经没了」的状态。
	go func() {
		_, _ = windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		p.teardown()
		_ = windows.CloseHandle(pi.Process)
		close(p.exited)
	}()
	return p, nil
}

var (
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procUpdateProcThreadAttribute = kernel32.NewProc("UpdateProcThreadAttribute")
)

// setPseudoConsoleAttr 把伪控制台挂到进程属性列表上。
//
// 这里直接调 UpdateProcThreadAttribute，而不是 x/sys 的封装：
// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE 的 lpValue 按 Win32 约定就是 HPCON 本身
// （微软官方示例也是直接传 hPC），封装函数要的却是 unsafe.Pointer，
// 写成 unsafe.Pointer(hpc) 会被 go vet 判成「uintptr 误用」。
// 实测传 &hpc 会让子进程挂不上控制台（CreateProcess 照样成功，shell 起来就退），
// 必须传句柄值本身。
func setPseudoConsoleAttr(attrs *windows.ProcThreadAttributeListContainer, hpc windows.Handle) error {
	ret, _, err := procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(attrs.List())),
		0,
		uintptr(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE),
		uintptr(hpc),
		unsafe.Sizeof(hpc),
		0,
		0,
	)
	if ret == 0 {
		return fmt.Errorf("绑定伪控制台失败: %w", err)
	}
	return nil
}

// createPseudoConsole 包一层 recover：Windows 7 / Server 2012 没有这个导出函数，
// 而 LazyProc 找不到符号时是 panic 而不是返回错误。
func createPseudoConsole(cols, rows uint16, in, out windows.Handle) (hpc windows.Handle, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("当前 Windows 不支持伪控制台，需要 Windows 10 1809 及以上版本")
		}
	}()
	err = windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, in, out, 0, &hpc)
	if err != nil {
		return 0, fmt.Errorf("创建伪控制台失败（需要 Windows 10 1809 及以上）: %w", err)
	}
	return hpc, nil
}

func (p *localPTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *localPTY) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *localPTY) Resize(cols, rows uint16) error {
	if p.hpc == 0 || cols == 0 || rows == 0 {
		return nil
	}
	return windows.ResizePseudoConsole(p.hpc, windows.Coord{X: int16(cols), Y: int16(rows)})
}

// teardown 关闭伪控制台并放掉输出管道的写端，让阻塞在 Read 上的 goroutine 收到 EOF。
func (p *localPTY) teardown() {
	p.tearOnce.Do(func() {
		if p.hpc != 0 {
			windows.ClosePseudoConsole(p.hpc)
		}
		_ = p.conOut.Close()
	})
}

func (p *localPTY) Close() error {
	p.closeOnce.Do(func() {
		// 先结束子进程，再拆控制台：反过来的话，还在跑的 shell 会收到
		// CTRL_CLOSE_EVENT 后继续往已销毁的管道写，偶尔弹出「程序已停止工作」。
		select {
		case <-p.exited:
		default:
			_ = windows.TerminateProcess(p.proc, 1)
			select {
			case <-p.exited:
			case <-time.After(3 * time.Second):
			}
		}
		p.teardown()
		_ = p.in.Close()
		_ = p.out.Close()
		_ = p.conIn.Close()
	})
	return nil
}
