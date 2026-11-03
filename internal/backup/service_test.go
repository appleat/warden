package backup

import (
	"compress/gzip"
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warden/internal/mysqldb"
	"warden/internal/mysqltest"
	"warden/internal/secure"
	"warden/internal/store"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

// newTestService 造一套「控制端 + 本机主机 + 已保存的 MySQL 连接」，
// 并把连库换成假驱动，避免测试依赖真实 MySQL。
func newTestService(t *testing.T) (*Service, *store.Store, *store.Host, *mysqltest.Server) {
	t.Helper()

	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "warden.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	host, err := st.EnsureLocalHost()
	if err != nil {
		t.Fatalf("创建本机主机失败: %v", err)
	}
	pass, _ := secure.Encrypt(testKey, "secret")
	if err := st.DB.Create(&store.MySQLConf{
		HostID: host.ID, Host: "127.0.0.1", Port: 3306,
		Username: "root", Password: pass,
	}).Error; err != nil {
		t.Fatalf("保存连接失败: %v", err)
	}

	fake := mysqltest.New("shop")
	// 与真实 MySQL 一致：查询里少了库限定名就报 1046
	fake.Strict = true
	fake.AddTable("users",
		"CREATE TABLE `users` (\n  `id` int NOT NULL,\n  `name` varchar(64)\n)",
		[]string{"id", "name"},
		[]string{"INT", "VARCHAR"},
		[][]driver.Value{
			{int64(1), []byte("张三")},
			{int64(2), []byte("李四")},
		})
	fake.AddTable("orders",
		"CREATE TABLE `orders` (\n  `id` int NOT NULL\n)",
		[]string{"id"},
		[]string{"INT"},
		[][]driver.Value{{int64(10)}},
	)

	orig := openDB
	openDB = func(mysqldb.Config) (*sql.DB, error) { return fake.DB(), nil }
	t.Cleanup(func() { openDB = orig })

	svc, err := New(st, nil, testKey, filepath.Join(root, "backups"))
	if err != nil {
		t.Fatalf("创建备份服务失败: %v", err)
	}
	// Windows 上 SQLite 文件被占用时临时目录删不掉，收尾先关连接池
	t.Cleanup(func() {
		if db, err := st.DB.DB(); err == nil {
			_ = db.Close()
		}
	})
	return svc, st, host, fake
}

func readBackup(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开备份文件失败: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("解压备份文件失败: %v", err)
	}
	defer gz.Close()
	b, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("读取备份内容失败: %v", err)
	}
	return string(b)
}

func TestRunWritesCompressedBackup(t *testing.T) {
	svc, st, host, _ := newTestService(t)

	task := &store.MySQLBackup{
		HostID: host.ID, Name: "订单库", Schema: "shop",
		Freq: FreqDaily, Hour: 2, Keep: 7, Compress: true, Enabled: true,
	}
	if err := st.DB.Create(task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	if err := svc.RunSync(context.Background(), task.ID); err != nil {
		t.Fatalf("执行备份失败: %v", err)
	}

	var got store.MySQLBackup
	if err := st.DB.First(&got, task.ID).Error; err != nil {
		t.Fatalf("读回任务失败: %v", err)
	}
	if got.LastStatus != "ok" {
		t.Fatalf("状态应为 ok，实际 %s（%s）", got.LastStatus, got.LastError)
	}
	if got.LastRows != 3 {
		t.Fatalf("行数应为 3，实际 %d", got.LastRows)
	}
	if got.LastSize <= 0 || got.LastFile == "" {
		t.Fatalf("文件信息缺失: %+v", got)
	}
	if !strings.HasSuffix(got.LastFile, ".sql.gz") {
		t.Fatalf("压缩时扩展名应为 .sql.gz，实际 %s", got.LastFile)
	}

	files, err := svc.Files(task.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("应有 1 个备份文件，实际 %d（%v）", len(files), err)
	}

	content := readBackup(t, filepath.Join(svc.TaskDir(task.ID), files[0].Name))
	for _, want := range []string{
		"CREATE DATABASE IF NOT EXISTS `shop`",
		"USE `shop`;",
		"DROP TABLE IF EXISTS `users`;",
		"INSERT INTO `users` (`id`, `name`) VALUES",
		"'张三'",
		"SET FOREIGN_KEY_CHECKS=1;",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("备份内容缺少 %q:\n%s", want, content)
		}
	}
}

func TestRunKeepsOnlyRecentFiles(t *testing.T) {
	svc, st, host, _ := newTestService(t)

	task := &store.MySQLBackup{
		HostID: host.ID, Name: "保留测试", Schema: "shop",
		Freq: FreqDaily, Keep: 2, Compress: true, Enabled: true,
	}
	st.DB.Create(task)

	for i := 0; i < 4; i++ {
		if err := svc.RunSync(context.Background(), task.ID); err != nil {
			t.Fatalf("第 %d 次备份失败: %v", i+1, err)
		}
	}
	files, err := svc.Files(task.ID)
	if err != nil {
		t.Fatalf("读取文件失败: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("保留 2 份，实际剩 %d 个文件", len(files))
	}
}

func TestRunSelectedTables(t *testing.T) {
	svc, st, host, _ := newTestService(t)

	task := &store.MySQLBackup{
		HostID: host.ID, Name: "只备份 users", Schema: "shop",
		Tables: []string{"users"}, Freq: FreqDaily, Keep: 0, Compress: false, Enabled: true,
	}
	st.DB.Create(task)

	// 从库里读回来，验证 TablesJSON ↔ Tables 的往返
	var loaded store.MySQLBackup
	st.DB.First(&loaded, task.ID)
	if len(loaded.Tables) != 1 || loaded.Tables[0] != "users" {
		t.Fatalf("表列表往返失败: %+v", loaded.Tables)
	}

	if err := svc.RunSync(context.Background(), task.ID); err != nil {
		t.Fatalf("执行备份失败: %v", err)
	}
	files, _ := svc.Files(task.ID)
	if len(files) != 1 {
		t.Fatalf("应有 1 个文件，实际 %d", len(files))
	}
	if !strings.HasSuffix(files[0].Name, ".sql") {
		t.Fatalf("不压缩时扩展名应为 .sql，实际 %s", files[0].Name)
	}
	raw, err := os.ReadFile(filepath.Join(svc.TaskDir(task.ID), files[0].Name))
	if err != nil {
		t.Fatalf("读取文件失败: %v", err)
	}
	if strings.Contains(string(raw), "`orders`") {
		t.Fatal("没选中的表不该出现在备份里")
	}
	if !strings.Contains(string(raw), "`users`") {
		t.Fatal("选中的表应出现在备份里")
	}
}

func TestRunFailsWithoutConnection(t *testing.T) {
	svc, st, _, _ := newTestService(t)

	// 一台没有 MySQL 连接配置的主机
	other := &store.Host{Name: "空主机", Kind: "agent", Address: "10.0.0.9:8090", Enabled: true}
	st.DB.Create(other)

	task := &store.MySQLBackup{
		HostID: other.ID, Name: "坏任务", Schema: "shop",
		Freq: FreqDaily, Keep: 3, Compress: true, Enabled: true,
	}
	st.DB.Create(task)

	if err := svc.RunSync(context.Background(), task.ID); err != nil {
		t.Fatalf("RunSync 不该返回错误（错误应落到任务状态里）: %v", err)
	}
	var got store.MySQLBackup
	st.DB.First(&got, task.ID)
	if got.LastStatus != "failed" {
		t.Fatalf("状态应为 failed，实际 %s", got.LastStatus)
	}
	if !strings.Contains(got.LastError, "MySQL 连接") {
		t.Fatalf("错误提示不友好: %s", got.LastError)
	}
	// 失败不能留下半成品文件
	files, _ := svc.Files(task.ID)
	if len(files) != 0 {
		t.Fatalf("失败时不该留下文件，实际 %d 个", len(files))
	}
}

func TestDeleteFileAndRemoveTask(t *testing.T) {
	svc, st, host, _ := newTestService(t)
	task := &store.MySQLBackup{
		HostID: host.ID, Name: "文件操作", Schema: "shop",
		Freq: FreqDaily, Keep: 0, Compress: true, Enabled: true,
	}
	st.DB.Create(task)
	if err := svc.RunSync(context.Background(), task.ID); err != nil {
		t.Fatalf("执行备份失败: %v", err)
	}
	files, _ := svc.Files(task.ID)
	if len(files) != 1 {
		t.Fatalf("应有 1 个文件，实际 %d", len(files))
	}

	// 路径穿越必须被挡住
	if _, err := svc.FilePath(task.ID, "../../etc/passwd"); err == nil {
		t.Fatal("应拒绝路径穿越")
	}
	if err := svc.DeleteFile(task.ID, files[0].Name); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}
	if left, _ := svc.Files(task.ID); len(left) != 0 {
		t.Fatalf("删除后应为空，实际 %d", len(left))
	}

	if err := svc.RemoveTask(task.ID); err != nil {
		t.Fatalf("清理任务目录失败: %v", err)
	}
	if _, err := os.Stat(svc.TaskDir(task.ID)); !os.IsNotExist(err) {
		t.Fatal("任务目录应被删除")
	}
}

func TestFileNameShape(t *testing.T) {
	task := &store.MySQLBackup{Schema: "my-shop", Compress: true}
	name := FileName(task, at(t, "2026-11-03 02:30"))
	if name != "my-shop_full_20261103-023000.sql.gz" {
		t.Fatalf("文件名不对: %s", name)
	}
	task.Tables = []string{"a", "b"}
	if got := FileName(task, at(t, "2026-11-03 02:30")); !strings.Contains(got, "_2tables_") {
		t.Fatalf("选表备份应标注表数: %s", got)
	}
	// 特殊字符要清洗掉
	if got := sanitize("库/名:*?"); strings.ContainsAny(got, `/\:*?`) {
		t.Fatalf("文件名未清洗: %s", got)
	}
}
