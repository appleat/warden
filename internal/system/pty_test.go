package system

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// 每个平台都必须至少能报出一个 shell，否则前端「本机终端」下拉会是空的。
func TestLocalShellsNotEmpty(t *testing.T) {
	opts := LocalShells()
	if len(opts) == 0 {
		t.Fatal("未探测到任何本机 shell")
	}
	for _, o := range opts {
		if o.ID == "" || o.Name == "" || o.Path == "" {
			t.Fatalf("shell 选项字段不完整: %+v", o)
		}
	}
	def := DefaultShellID()
	found := false
	for _, o := range opts {
		if o.ID == def {
			found = true
		}
	}
	if !found {
		t.Fatalf("默认 shell %q 不在可选列表 %v 中", def, opts)
	}
}

// 打开真终端，敲一条命令，能读到回显与输出才算通。
// 这条用例覆盖的是「pty 真的建起来了」——单元测试里最容易假绿的地方。
func TestLocalShellRoundTrip(t *testing.T) {
	term, err := StartLocalShell("")
	if err != nil {
		t.Fatalf("启动本机 shell 失败: %v", err)
	}
	defer term.Close()

	if err := term.Resize(120, 30); err != nil {
		t.Fatalf("调整终端尺寸失败: %v", err)
	}

	r := newPtyReader(term)

	// 先等第一屏（提示符），再敲命令，避免把输入回显误当成命令输出
	if !r.waitLen(1, 10*time.Second) {
		t.Fatal("没有收到任何终端输出，shell 可能没真正启动")
	}
	before := r.len()

	if _, err := term.Write([]byte("echo warden-pty-ok\r\n")); err != nil {
		t.Fatalf("写入终端失败: %v", err)
	}
	if !r.waitFor("warden-pty-ok", 10*time.Second) {
		t.Fatalf("没读到命令输出，实际收到: %q", r.text())
	}
	if r.len() <= before {
		t.Fatalf("敲了命令但输出没有增长，实际收到: %q", r.text())
	}
}

// ptyReader 持续读终端并缓存，避免多次调用产生多个读 goroutine 抢同一个 fd。
type ptyReader struct {
	mu   sync.Mutex
	buf  []byte
	done chan struct{}
}

func newPtyReader(term Term) *ptyReader {
	r := &ptyReader{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		b := make([]byte, 4096)
		for {
			n, err := term.Read(b)
			if n > 0 {
				r.mu.Lock()
				r.buf = append(r.buf, b[:n]...)
				r.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return r
}

func (r *ptyReader) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

func (r *ptyReader) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}

func (r *ptyReader) waitFor(want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if strings.Contains(r.text(), want) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-r.done:
			return strings.Contains(r.text(), want)
		case <-time.After(30 * time.Millisecond):
		}
	}
}

func (r *ptyReader) waitLen(n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if r.len() >= n {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-r.done:
			return r.len() >= n
		case <-time.After(30 * time.Millisecond):
		}
	}
}
