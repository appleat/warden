package filemgr

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Backend 是文件操作后端的抽象：本地磁盘（Service）与远程 SFTP 都实现它，
// 这样文件管理的 HTTP 接口可以在「本机」与「远程 SSH 主机」之间无缝切换。
type Backend interface {
	List(p string) (string, []Entry, error)
	Mkdir(p, name string, perm os.FileMode) (string, error)
	CreateFile(p, name string) (string, error)
	Rename(from, to string) error
	Copy(from, to string) error
	Chmod(p string, mode os.FileMode) error
	Delete(p string) error
	Read(p string) (string, int64, error)
	Write(p, content string) error
	Stat(p string) (os.FileInfo, error)
	Open(p string) (io.ReadCloser, error)
	SaveUpload(dir, filename string, r io.Reader) (string, int64, error)
	Compress(p, format string) (string, error)
	Extract(p, dest string) error
	RootDisplay() string
	Close() error
}

// Service 提供受限根目录内的文件操作，所有路径都必须落在 Root 之内。
type Service struct {
	root string
}

func New(root string) (*Service, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return &Service{root: abs}, nil
}

func (s *Service) Root() string { return s.root }

// RootDisplay 返回用于前端展示的根目录（统一用 / 分隔，与 List 返回的 path 一致）。
func (s *Service) RootDisplay() string { return filepath.ToSlash(s.root) }

// RootPrefix 用于前端拼接展示。
func (s *Service) rootPrefix() string {
	if strings.HasSuffix(s.root, string(os.PathSeparator)) {
		return s.root
	}
	return s.root + string(os.PathSeparator)
}

// Resolve 把前端传入的路径解析为受控绝对路径，越界即报错。
func (s *Service) Resolve(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimSpace(p)
	if p == "" {
		return s.root, nil
	}
	// Windows 上 "D:" 是「盘符相对路径」，IsAbs 为 false，直接拼到根目录下会得到
	// `D:\D:` 这种非法路径（前端点「上一级」退到盘符根时就会踩到）。补上分隔符即可。
	if len(p) == 2 && p[1] == ':' && isASCIILetter(p[0]) {
		p += "/"
	}
	var abs string
	if filepath.IsAbs(filepath.FromSlash(p)) {
		abs = filepath.Clean(filepath.FromSlash(p))
	} else {
		abs = filepath.Clean(filepath.Join(s.root, filepath.FromSlash(p)))
	}
	if !s.withinRoot(abs) {
		return "", errors.New("路径超出允许访问的范围")
	}
	// 防符号链接逃逸：已存在的路径做真实路径校验
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		if !s.withinRoot(real) {
			return "", errors.New("路径指向了根目录之外")
		}
	}
	return abs, nil
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// withinRoot 判断路径是否落在根目录内。Windows 文件名大小写不敏感，比较时统一转小写。
func (s *Service) withinRoot(p string) bool {
	root := s.root
	prefix := s.rootPrefix()
	if runtime.GOOS == "windows" {
		p, root, prefix = strings.ToLower(p), strings.ToLower(root), strings.ToLower(prefix)
	}
	return p == root || strings.HasPrefix(p, prefix)
}

type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Dir     bool   `json:"dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModeBit uint32 `json:"mode_bit"`
	ModTime int64  `json:"mod_time"`
	Hidden  bool   `json:"hidden"`
	Link    bool   `json:"link"`
}

func (s *Service) List(p string) (string, []Entry, error) {
	abs, err := s.Resolve(p)
	if err != nil {
		return "", nil, err
	}
	items, err := os.ReadDir(abs)
	if err != nil {
		return "", nil, err
	}
	out := make([]Entry, 0, len(items))
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue
		}
		name := it.Name()
		out = append(out, Entry{
			Name:    name,
			Path:    s.displayPath(filepath.Join(abs, name)),
			Dir:     it.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModeBit: uint32(info.Mode().Perm()),
			ModTime: info.ModTime().Unix(),
			Hidden:  strings.HasPrefix(name, "."),
			Link:    info.Mode()&os.ModeSymlink != 0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return s.displayPath(abs), out, nil
}

func (s *Service) displayPath(abs string) string {
	return filepath.ToSlash(abs)
}

func (s *Service) Mkdir(p string, name string, perm os.FileMode) (string, error) {
	dir, err := s.Resolve(p)
	if err != nil {
		return "", err
	}
	if err := validateName(name); err != nil {
		return "", err
	}
	target := filepath.Join(dir, filepath.FromSlash(name))
	if _, err := os.Stat(target); err == nil {
		return "", errors.New("同名文件或目录已存在")
	}
	if perm == 0 {
		perm = 0o755
	}
	return s.displayPath(target), os.MkdirAll(target, perm)
}

func (s *Service) CreateFile(p string, name string) (string, error) {
	dir, err := s.Resolve(p)
	if err != nil {
		return "", err
	}
	if err := validateName(name); err != nil {
		return "", err
	}
	target := filepath.Join(dir, filepath.FromSlash(name))
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	f.Close()
	return s.displayPath(target), nil
}

func (s *Service) Rename(from, to string) error {
	src, err := s.Resolve(from)
	if err != nil {
		return err
	}
	var dst string
	if strings.ContainsAny(to, "/\\") {
		dst, err = s.Resolve(to)
	} else {
		dst = filepath.Join(filepath.Dir(src), filepath.FromSlash(to))
	}
	if err != nil {
		return err
	}
	if src == s.root {
		return errors.New("不能重命名根目录")
	}
	if _, err := os.Stat(dst); err == nil {
		return errors.New("目标已存在")
	}
	return os.Rename(src, dst)
}

func (s *Service) Copy(from, to string) error {
	src, err := s.Resolve(from)
	if err != nil {
		return err
	}
	var dst string
	if strings.ContainsAny(to, "/\\") {
		dst, err = s.Resolve(to)
	} else {
		dst = filepath.Join(filepath.Dir(src), filepath.FromSlash(to))
	}
	if err != nil {
		return err
	}
	if src == dst {
		return errors.New("源与目标相同")
	}
	return copyTree(src, dst)
}

func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		items, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := copyTree(filepath.Join(src, it.Name()), filepath.Join(dst, it.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	return os.Rename(tmp, dst)
}

func (s *Service) Delete(p string) error {
	abs, err := s.Resolve(p)
	if err != nil {
		return err
	}
	if abs == s.root {
		return errors.New("不能删除根目录")
	}
	return os.RemoveAll(abs)
}

func (s *Service) Chmod(p string, mode os.FileMode) error {
	abs, err := s.Resolve(p)
	if err != nil {
		return err
	}
	return os.Chmod(abs, mode)
}

const maxEditSize = 4 << 20 // 4MB

func (s *Service) Read(p string) (string, int64, error) {
	abs, err := s.Resolve(p)
	if err != nil {
		return "", 0, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", 0, err
	}
	if info.IsDir() {
		return "", 0, errors.New("目标是目录，无法编辑")
	}
	if info.Size() > maxEditSize {
		return "", info.Size(), errors.New("文件超过 4MB，请下载后编辑")
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", 0, err
	}
	return string(b), info.Size(), nil
}

func (s *Service) Write(p, content string) error {
	abs, err := s.Resolve(p)
	if err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

func (s *Service) Open(p string) (io.ReadCloser, error) {
	abs, err := s.Resolve(p)
	if err != nil {
		return nil, err
	}
	return os.Open(abs)
}

// Stat 返回文件元信息，供下载前获取大小/修改时间。
func (s *Service) Stat(p string) (os.FileInfo, error) {
	abs, err := s.Resolve(p)
	if err != nil {
		return nil, err
	}
	return os.Stat(abs)
}

// Close 本地后端无需释放资源，SFTP 后端会断开连接。
func (s *Service) Close() error { return nil }

// SaveUpload 把上传流写入目标目录，同名文件自动加序号。
func (s *Service) SaveUpload(dir, filename string, r io.Reader) (string, int64, error) {
	d, err := s.Resolve(dir)
	if err != nil {
		return "", 0, err
	}
	name := filepath.Base(filepath.FromSlash(strings.ReplaceAll(filename, "\\", "/")))
	if name == "" || name == "." || name == ".." {
		return "", 0, errors.New("文件名不合法")
	}
	target := uniquePath(filepath.Join(d, name))
	f, err := os.Create(target)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		os.Remove(target)
		return "", 0, err
	}
	return s.displayPath(target), n, nil
}

func uniquePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s(%d)%s", base, i, ext)
		if _, err := os.Stat(cand); err != nil {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d%s", base, os.Getpid(), ext)
}

// ---------- 压缩 / 解压 ----------

func (s *Service) Compress(p, format string) (string, error) {
	abs, err := s.Resolve(p)
	if err != nil {
		return "", err
	}
	if abs == s.root {
		return "", errors.New("不能压缩根目录")
	}
	var out string
	switch format {
	case "zip":
		out = abs + ".zip"
	case "targz", "":
		out = abs + ".tar.gz"
	default:
		return "", errors.New("仅支持 zip 与 tar.gz")
	}
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if format == "zip" {
		zw := zip.NewWriter(f)
		if err := walkAndZip(zw, abs, filepath.Base(abs)); err != nil {
			zw.Close()
			return "", err
		}
		if err := zw.Close(); err != nil {
			return "", err
		}
	} else {
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		if err := walkAndTar(tw, abs, filepath.Base(abs)); err != nil {
			tw.Close()
			gz.Close()
			return "", err
		}
		tw.Close()
		gz.Close()
	}
	return s.displayPath(out), nil
}

func walkAndZip(zw *zip.Writer, src, base string) error {
	return filepath.Walk(src, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(src), p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			_, err = zw.Create(rel + "/")
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}

func walkAndTar(tw *tar.Writer, src, base string) error {
	return filepath.Walk(src, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(src), p)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

func (s *Service) Extract(p, dest string) error {
	abs, err := s.Resolve(p)
	if err != nil {
		return err
	}
	d, err := s.Resolve(dest)
	if err != nil {
		return err
	}
	if d == "" {
		d = filepath.Dir(abs)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	lower := strings.ToLower(abs)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(abs, d)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(abs)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		return extractTar(gz, d)
	case strings.HasSuffix(lower, ".tar"):
		f, err := os.Open(abs)
		if err != nil {
			return err
		}
		defer f.Close()
		return extractTar(f, d)
	default:
		return errors.New("仅支持 zip / tar / tar.gz")
	}
}

func extractZip(src, dest string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(target), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode().Perm())
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0o755)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

// safeJoin 阻止 zip slip。
func safeJoin(dest, name string) (string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	target := filepath.Join(dest, filepath.FromSlash(clean))
	if target != dest && !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
		return "", fmt.Errorf("压缩包内含非法路径: %s", name)
	}
	return target, nil
}

func validateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return errors.New("名称不合法")
	}
	if strings.ContainsAny(name, "/\\") {
		return errors.New("名称不能包含路径分隔符")
	}
	return nil
}
