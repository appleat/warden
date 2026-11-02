package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseBool(t *testing.T) {
	cases := []struct {
		in   string
		def  bool
		want bool
	}{
		{"1", false, true},
		{"true", false, true},
		{"TRUE", false, true},
		{" yes ", false, true},
		{"on", false, true},
		{"0", true, false},
		{"false", true, false},
		{"off", true, false},
		{"n", true, false},
		{"随便写点啥", true, true}, // 认不出来保留默认
		{"", false, false},
	}
	for _, c := range cases {
		if got := parseBool(c.in, c.def); got != c.want {
			t.Fatalf("parseBool(%q, %v) = %v，期望 %v", c.in, c.def, got, c.want)
		}
	}
}

// 自动放行端口默认开启，配置文件能关掉，环境变量优先级更高。
func TestAutoOpenPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("WARDEN_CONFIG", path)
	t.Setenv("WARDEN_AUTO_OPEN_PORT", "")
	t.Setenv("WARDEN_MODE", "agent")

	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 1) 配置里没写 -> 默认开启
	write("mode: agent\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutoOpenPort {
		t.Fatal("未配置时应默认开启自动放行")
	}

	// 2) 显式关闭
	write("mode: agent\nauto_open_port: false\n")
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.AutoOpenPort {
		t.Fatal("auto_open_port: false 应生效")
	}

	// 3) 环境变量覆盖配置文件
	write("mode: agent\nauto_open_port: true\n")
	t.Setenv("WARDEN_AUTO_OPEN_PORT", "off")
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.AutoOpenPort {
		t.Fatal("WARDEN_AUTO_OPEN_PORT=off 应覆盖配置文件")
	}
}
