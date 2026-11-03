//go:build windows

package system

import (
	"strings"
	"testing"
)

// Windows 上「本机终端」必须是真正的 PowerShell / CMD，而不是某个 POSIX shell。
func TestResolveShellOnWindows(t *testing.T) {
	cases := []struct {
		in      string
		wantExe string
	}{
		{"", "powershell.exe"}, // 默认给 PowerShell
		{"powershell", "powershell.exe"},
		{"cmd", "cmd.exe"},
		{"CMD", "cmd.exe"}, // 大小写不敏感
	}
	for _, c := range cases {
		spec, err := resolveShell(c.in)
		if err != nil {
			t.Fatalf("resolveShell(%q) 失败: %v", c.in, err)
		}
		if !strings.EqualFold(baseName(spec.path), c.wantExe) {
			t.Fatalf("resolveShell(%q) 得到 %q，期望 %q", c.in, spec.path, c.wantExe)
		}
	}
}

// CMD 需要把代码页切到 UTF-8，否则中文输出在 xterm 里是乱码。
func TestCmdSetsUTF8CodePage(t *testing.T) {
	spec, err := resolveShell("cmd")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.args, " ")
	if !strings.Contains(joined, "65001") {
		t.Fatalf("cmd 启动参数应包含 chcp 65001，实际 %v", spec.args)
	}
}

func TestResolveShellRejectsUnknown(t *testing.T) {
	if _, err := resolveShell("no-such-shell-xyz"); err == nil {
		t.Fatal("未知 shell 应当报错")
	}
	if _, err := resolveShell(`C:\definitely\not\here.exe`); err == nil {
		t.Fatal("不存在的绝对路径应当报错")
	}
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}
