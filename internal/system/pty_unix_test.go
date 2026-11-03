//go:build !windows

package system

import (
	"os"
	"testing"
)

// shell 参数既支持名字（bash）也支持绝对路径（config.yaml 里的 /usr/bin/zsh）。
func TestResolveUnixShell(t *testing.T) {
	if got := resolveUnixShell("/bin/sh"); got != "/bin/sh" {
		t.Fatalf("绝对路径应原样使用，实际 %q", got)
	}
	got := resolveUnixShell("")
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("默认 shell %q 不可用: %v", got, err)
	}
	// 不存在的名字要退回一个能用的 shell，而不是让「打开终端」直接失败
	if got := resolveUnixShell("no-such-shell-xyz"); got == "" {
		t.Fatal("未知 shell 应退回可用的 sh")
	} else if _, err := os.Stat(got); err != nil {
		t.Fatalf("退回的 shell %q 不可用: %v", got, err)
	}
}

func TestResolveUnixShellByName(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("没有 /bin/sh")
	}
	got := resolveUnixShell("sh")
	if got == "" {
		t.Fatal("按名字查找 sh 失败")
	}
}
