package filemgr

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	svc, err := New(root)
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	return svc, root
}

func TestResolveRejectsEscape(t *testing.T) {
	svc, _ := newTestService(t)
	bad := []string{"../etc/passwd", "../../", "a/../../b", ".."}
	for _, p := range bad {
		if _, err := svc.Resolve(p); err == nil {
			t.Fatalf("路径 %q 应当被拒绝", p)
		}
	}
}

func TestResolveAcceptsInside(t *testing.T) {
	svc, root := newTestService(t)
	abs, err := svc.Resolve("sub/file.txt")
	if err != nil {
		t.Fatalf("正常路径不应报错: %v", err)
	}
	if !strings.HasPrefix(abs, root) {
		t.Fatalf("解析结果越界: %s", abs)
	}
	if _, err := svc.Resolve(root); err != nil {
		t.Fatalf("根目录本身应当允许: %v", err)
	}
}

// 前端点「上一级」退到盘符根时会传 "D:"，必须解析成盘符根而不是 `D:\D:`。
func TestResolveWindowsDriveRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 需要校验盘符根路径")
	}
	vol := filepath.VolumeName(t.TempDir()) // 形如 "C:"
	svc, err := New(vol + `\`)
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	for _, p := range []string{vol, vol + "/", strings.ToUpper(vol)} {
		abs, err := svc.Resolve(p)
		if err != nil {
			t.Fatalf("盘符根 %q 不应报错: %v", p, err)
		}
		if abs != svc.Root() {
			t.Fatalf("盘符根 %q 解析结果应为 %q，实际 %q", p, svc.Root(), abs)
		}
	}
	if got := svc.RootDisplay(); strings.Contains(got, `\`) {
		t.Fatalf("展示用根目录应使用 / 分隔，实际 %q", got)
	}
}

func TestMkdirWriteReadDelete(t *testing.T) {
	svc, _ := newTestService(t)

	if _, err := svc.Mkdir("", "logs", 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if _, err := svc.CreateFile("logs", "a.txt"); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	if err := svc.Write("logs/a.txt", "hello 世界"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	content, size, err := svc.Read("logs/a.txt")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if content != "hello 世界" || size == 0 {
		t.Fatalf("内容不一致: %q", content)
	}

	_, items, err := svc.List("logs")
	if err != nil || len(items) != 1 {
		t.Fatalf("列表异常: %v len=%d", err, len(items))
	}
	if items[0].Name != "a.txt" || items[0].Dir {
		t.Fatalf("条目信息不正确: %+v", items[0])
	}

	// 重名创建应报错
	if _, err := svc.Mkdir("", "logs", 0o755); err == nil {
		t.Fatal("重复创建目录应当失败")
	}

	if err := svc.Delete("logs"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(svc.root, "logs")); !os.IsNotExist(err) {
		t.Fatal("目录应当已被删除")
	}
}

func TestDeleteRootRejected(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Delete(""); err == nil {
		t.Fatal("不允许删除根目录")
	}
}

func TestSafeJoinBlocksZipSlip(t *testing.T) {
	dest := t.TempDir()
	// 穿越路径会被重新锚定到 dest 内，而不是逃逸出去
	if p, err := safeJoin(dest, "../../evil.sh"); err != nil || !strings.HasPrefix(p, dest) {
		t.Fatalf("穿越路径必须落在 dest 内: %s %v", p, err)
	}
	if p, err := safeJoin(dest, `..\..\evil.sh`); err != nil || !strings.HasPrefix(p, dest) {
		t.Fatalf("反斜杠穿越同样必须被收敛: %s %v", p, err)
	}
	if _, err := safeJoin(dest, "/etc/passwd"); err != nil {
		t.Fatalf("绝对路径应被重定位到 dest 内: %v", err)
	}
	ok, err := safeJoin(dest, "a/b/c.txt")
	if err != nil || !strings.HasPrefix(ok, dest) {
		t.Fatalf("正常路径解析异常: %v", err)
	}
}

func TestCompressAndExtract(t *testing.T) {
	svc, root := newTestService(t)
	if _, err := svc.Mkdir("", "src", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := svc.Write("src/x.txt", "data"); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Compress("src", "zip")
	if err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "src.zip")); err != nil {
		t.Fatalf("压缩包不存在: %v", err)
	}
	if err := svc.Extract(out, "out"); err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "out", "src", "x.txt")); err != nil {
		t.Fatalf("解压结果缺失: %v", err)
	}
}
