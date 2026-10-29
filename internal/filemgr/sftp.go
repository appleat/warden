package filemgr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"warden/internal/system"
)

// SFTPBackend 通过 SSH 的 SFTP 子系统操作远程主机文件系统。
// 它实现 Backend 接口，使文件管理的 HTTP 接口能在「本机」与「远程 SSH 主机」间切换。
type SFTPBackend struct {
	client *ssh.Client
	sftp   *sftp.Client
}

// NewSFTP 建立到远程主机的 SSH 连接并打开 SFTP 会话。调用方使用完毕后需 Close。
func NewSFTP(t system.SSHTarget) (*SFTPBackend, error) {
	cli, err := system.DialSSH(t, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	s, err := sftp.NewClient(cli)
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("SFTP 初始化失败: %w", err)
	}
	return &SFTPBackend{client: cli, sftp: s}, nil
}

func (b *SFTPBackend) Close() error {
	err := b.sftp.Close()
	cerr := b.client.Close()
	if err != nil {
		return err
	}
	return cerr
}

// resolve 把任意输入路径规整为远程绝对路径，并把 ".." 钳制在根目录下，
// 防止越界到文件系统根之上。远端不强制根目录限制（由 SSH 用户权限约束）。
func (b *SFTPBackend) resolve(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	clean := path.Clean(p)
	if clean == "." || clean == "" {
		return "/"
	}
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	return clean
}

func (b *SFTPBackend) RootDisplay() string { return "/" }

func (b *SFTPBackend) List(p string) (string, []Entry, error) {
	abs := b.resolve(p)
	infos, err := b.sftp.ReadDir(abs)
	if err != nil {
		return "", nil, err
	}
	out := make([]Entry, 0, len(infos))
	for _, it := range infos {
		name := it.Name()
		var full string
		if abs == "/" {
			full = "/" + name
		} else {
			full = abs + "/" + name
		}
		out = append(out, Entry{
			Name:    name,
			Path:    full,
			Dir:     it.IsDir(),
			Size:    it.Size(),
			Mode:    it.Mode().String(),
			ModeBit: uint32(it.Mode().Perm()),
			ModTime: it.ModTime().Unix(),
			Hidden:  strings.HasPrefix(name, "."),
			Link:    it.Mode()&os.ModeSymlink != 0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return abs, out, nil
}

func (b *SFTPBackend) Stat(p string) (os.FileInfo, error) {
	return b.sftp.Stat(b.resolve(p))
}

func (b *SFTPBackend) Open(p string) (io.ReadCloser, error) {
	return b.sftp.Open(b.resolve(p))
}

func (b *SFTPBackend) Read(p string) (string, int64, error) {
	abs := b.resolve(p)
	info, err := b.sftp.Stat(abs)
	if err != nil {
		return "", 0, err
	}
	if info.IsDir() {
		return "", 0, errors.New("目标是目录，无法编辑")
	}
	if info.Size() > maxEditSize {
		return "", info.Size(), errors.New("文件超过 4MB，请下载后编辑")
	}
	f, err := b.sftp.Open(abs)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	buf, err := io.ReadAll(f)
	if err != nil {
		return "", 0, err
	}
	return string(buf), info.Size(), nil
}

func (b *SFTPBackend) Write(p, content string) error {
	abs := b.resolve(p)
	f, err := b.sftp.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write([]byte(content)); err != nil {
		return err
	}
	_ = b.sftp.Chmod(abs, 0o644)
	return nil
}

func (b *SFTPBackend) mkdirAll(dir string) error {
	dir = b.resolve(dir)
	if dir == "/" {
		return nil
	}
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	cur := ""
	for _, part := range parts {
		cur = cur + "/" + part
		if _, err := b.sftp.Stat(cur); err == nil {
			continue
		}
		if err := b.sftp.Mkdir(cur); err != nil {
			return err
		}
	}
	return nil
}

func (b *SFTPBackend) Mkdir(p, name string, perm os.FileMode) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	if perm == 0 {
		perm = 0o755
	}
	target := path.Join(b.resolve(p), name)
	if err := b.mkdirAll(target); err != nil {
		return "", err
	}
	_ = b.sftp.Chmod(target, perm)
	return target, nil
}

func (b *SFTPBackend) CreateFile(p, name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	target := path.Join(b.resolve(p), name)
	if _, err := b.sftp.Stat(target); err == nil {
		return "", errors.New("同名文件或目录已存在")
	}
	f, err := b.sftp.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return "", err
	}
	f.Close()
	_ = b.sftp.Chmod(target, 0o644)
	return target, nil
}

func (b *SFTPBackend) Rename(from, to string) error {
	src := b.resolve(from)
	var dst string
	if strings.ContainsAny(to, "/\\") {
		dst = b.resolve(to)
	} else {
		dst = path.Join(path.Dir(src), to)
	}
	if src == "/" {
		return errors.New("不能重命名根目录")
	}
	if _, err := b.sftp.Stat(dst); err == nil {
		return errors.New("目标已存在")
	}
	return b.sftp.Rename(src, dst)
}

func (b *SFTPBackend) Chmod(p string, mode os.FileMode) error {
	return b.sftp.Chmod(b.resolve(p), mode)
}

func (b *SFTPBackend) Delete(p string) error {
	abs := b.resolve(p)
	if abs == "/" {
		return errors.New("不能删除根目录")
	}
	return b.removeAll(abs)
}

func (b *SFTPBackend) removeAll(p string) error {
	info, err := b.sftp.Stat(p)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return b.sftp.Remove(p)
	}
	entries, err := b.sftp.ReadDir(p)
	if err != nil {
		return err
	}
	for _, e := range entries {
		child := path.Join(p, e.Name())
		if e.IsDir() {
			if err := b.removeAll(child); err != nil {
				return err
			}
		} else {
			if err := b.sftp.Remove(child); err != nil {
				return err
			}
		}
	}
	return b.sftp.RemoveDirectory(p)
}

func (b *SFTPBackend) SaveUpload(dir, filename string, r io.Reader) (string, int64, error) {
	d := b.resolve(dir)
	name := filepath.Base(filepath.FromSlash(strings.ReplaceAll(filename, "\\", "/")))
	if name == "" || name == "." || name == ".." {
		return "", 0, errors.New("文件名不合法")
	}
	target := b.uniquePath(path.Join(d, name))
	f, err := b.sftp.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		return "", 0, err
	}
	_ = b.sftp.Chmod(target, 0o644)
	return target, n, err
}

func (b *SFTPBackend) uniquePath(p string) string {
	if _, err := b.sftp.Stat(p); err != nil {
		return p
	}
	ext := path.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s(%d)%s", base, i, ext)
		if _, err := b.sftp.Stat(cand); err != nil {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d%s", base, os.Getpid(), ext)
}

func (b *SFTPBackend) Copy(from, to string) error {
	src := b.resolve(from)
	var dst string
	if strings.ContainsAny(to, "/\\") {
		dst = b.resolve(to)
	} else {
		dst = path.Join(path.Dir(src), to)
	}
	if src == dst {
		return errors.New("源与目标相同")
	}
	return b.copyTree(src, dst)
}

func (b *SFTPBackend) copyTree(src, dst string) error {
	info, err := b.sftp.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := b.mkdirAll(dst); err != nil {
			return err
		}
		entries, err := b.sftp.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := b.copyTree(path.Join(src, e.Name()), path.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	rf, err := b.sftp.Open(src)
	if err != nil {
		return err
	}
	defer rf.Close()
	wf, err := b.sftp.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	defer wf.Close()
	if _, err = io.Copy(wf, rf); err != nil {
		return err
	}
	_ = b.sftp.Chmod(dst, info.Mode().Perm())
	return nil
}

// Compress / Extract 在远端没有原生 SFTP 接口，复用已建立的 SSH 连接执行命令完成。
// 远端需具备 tar（以及 zip 压缩所需的 zip/unzip）。

func (b *SFTPBackend) Compress(p, format string) (string, error) {
	abs := b.resolve(p)
	if abs == "/" {
		return "", errors.New("不能压缩根目录")
	}
	base := path.Base(abs)
	parent := b.resolve(path.Dir(abs))
	var cmd string
	switch format {
	case "zip":
		cmd = fmt.Sprintf("cd %s && zip -r -- %s.zip %s", shellQuote(parent), shellQuote(base), shellQuote(base))
	default:
		cmd = fmt.Sprintf("cd %s && tar -czf -- %s.tar.gz %s", shellQuote(parent), shellQuote(base), shellQuote(base))
	}
	out, err := b.run(cmd, 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("压缩失败: %s (%v)", strings.TrimSpace(out), err)
	}
	if format == "zip" {
		return path.Join(parent, base+".zip"), nil
	}
	return path.Join(parent, base+".tar.gz"), nil
}

func (b *SFTPBackend) Extract(p, dest string) error {
	abs := b.resolve(p)
	d := b.resolve(dest)
	if d == "/" {
		// 解压到根或留空时，回退到压缩包所在目录，避免误写入根
		d = path.Dir(abs)
	}
	if err := b.mkdirAll(d); err != nil {
		return err
	}
	lower := strings.ToLower(abs)
	var cmd string
	switch {
	case strings.HasSuffix(lower, ".zip"):
		cmd = fmt.Sprintf("cd %s && unzip -- %s", shellQuote(d), shellQuote(abs))
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		cmd = fmt.Sprintf("cd %s && tar -xzf -- %s", shellQuote(d), shellQuote(abs))
	case strings.HasSuffix(lower, ".tar"):
		cmd = fmt.Sprintf("cd %s && tar -xf -- %s", shellQuote(d), shellQuote(abs))
	default:
		return errors.New("仅支持 zip / tar / tar.gz")
	}
	out, err := b.run(cmd, 3*time.Minute)
	if err != nil {
		return fmt.Errorf("解压失败: %s (%v)", strings.TrimSpace(out), err)
	}
	return nil
}

func (b *SFTPBackend) run(cmd string, timeout time.Duration) (string, error) {
	sess, err := b.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var buf bytes.Buffer
	sess.Stdout = &buf
	sess.Stderr = &buf
	if err := sess.Start(cmd); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-time.After(timeout):
		_ = sess.Signal(ssh.SIGKILL)
		return buf.String(), errors.New("命令执行超时")
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
