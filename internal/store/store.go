package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ---------- 数据模型 ----------

type User struct {
	ID          uint       `gorm:"primarykey" json:"id"`
	Username    string     `gorm:"uniqueIndex;size:64;not null" json:"username"`
	Password    string     `gorm:"size:255;not null" json:"-"`
	Role        string     `gorm:"size:16;not null;default:admin" json:"role"`
	TOTPSecret  string     `gorm:"size:128" json:"-"`
	TOTPEnabled bool       `gorm:"default:false" json:"totp_enabled"`
	Disabled    bool       `gorm:"default:false" json:"disabled"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type AuditLog struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Username  string    `gorm:"size:64;index" json:"username"`
	Action    string    `gorm:"size:64;index" json:"action"`
	Target    string    `gorm:"size:255" json:"target"`
	Detail    string    `gorm:"type:text" json:"detail"`
	IP        string    `gorm:"size:64" json:"ip"`
	Result    string    `gorm:"size:16" json:"result"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

type Monitor struct {
	ID           uint      `gorm:"primarykey" json:"id"`
	Name         string    `gorm:"size:128;not null" json:"name"`
	Type         string    `gorm:"size:16;not null;default:http" json:"type"` // http | tcp | keyword
	Target       string    `gorm:"size:512;not null" json:"target"`           // URL 或 host:port
	Method       string    `gorm:"size:8;default:GET" json:"method"`
	ExpectCode   int       `gorm:"default:200" json:"expect_code"`
	Keyword      string    `gorm:"size:255" json:"keyword"`
	Interval     int       `gorm:"default:60" json:"interval"` // 秒
	Timeout      int       `gorm:"default:10" json:"timeout"`  // 秒
	FailRetries  int       `gorm:"default:2" json:"fail_retries"`
	Enabled      bool      `gorm:"default:true" json:"enabled"`
	GroupName    string    `gorm:"size:64" json:"group_name"`
	Remark       string    `gorm:"size:255" json:"remark"`
	CertWarnDays int       `gorm:"default:14" json:"cert_warn_days"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// 运行时态（不落库）
	Status      string     `gorm:"-" json:"status"`
	LatencyMs   int64      `gorm:"-" json:"latency_ms"`
	LastCode    int        `gorm:"-" json:"last_code"`
	LastMessage string     `gorm:"-" json:"last_message"`
	LastCheckAt *time.Time `gorm:"-" json:"last_check_at"`
	Uptime      float64    `gorm:"-" json:"uptime"`
	CertDays    int        `gorm:"-" json:"cert_days"`
}

type MonitorResult struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	MonitorID  uint      `gorm:"index:idx_mid_time" json:"monitor_id"`
	OK         bool      `json:"ok"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int64     `json:"latency_ms"`
	Message    string    `gorm:"size:255" json:"message"`
	CreatedAt  time.Time `gorm:"index:idx_mid_time" json:"created_at"`
}

type SSHHost struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	Name       string    `gorm:"size:128;not null" json:"name"`
	Host       string    `gorm:"size:255;not null" json:"host"`
	Port       int       `gorm:"default:22" json:"port"`
	Username   string    `gorm:"size:64;not null" json:"username"`
	AuthType   string    `gorm:"size:16;default:password" json:"auth_type"` // password | key
	Secret     string    `gorm:"type:text" json:"-"`
	Passphrase string    `gorm:"type:text" json:"-"`
	Remark     string    `gorm:"size:255" json:"remark"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Setting struct {
	Key   string `gorm:"primarykey;size:64" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// Host 是被管理的主机。Kind=local 表示控制端所在机器，仅有一条。
type Host struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	Name      string    `gorm:"size:128;not null" json:"name"`
	Kind      string    `gorm:"size:16;not null;default:agent" json:"kind"` // local | agent
	Address   string    `gorm:"size:255" json:"address"`                    // 1.2.3.4:8090
	TLS       bool      `gorm:"default:false" json:"tls"`
	Insecure  bool      `gorm:"default:false" json:"insecure"` // 跳过证书校验
	Token     string    `gorm:"type:text" json:"-"`
	GroupName string    `gorm:"size:64" json:"group_name"`
	Remark    string    `gorm:"size:255" json:"remark"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NacosConf 是某台主机上 Nacos 服务的连接配置（控制端保存，按主机隔离）。
// Password 加密落库，接口里不返回明文。
type NacosConf struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	HostID    uint      `gorm:"uniqueIndex;not null" json:"host_id"`
	Addr      string    `gorm:"size:255" json:"addr"`
	Username  string    `gorm:"size:128" json:"username"`
	Password  string    `gorm:"type:text" json:"-"`
	Namespace string    `gorm:"size:128" json:"namespace"`
	Insecure  bool      `gorm:"default:false" json:"insecure"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RedisConf 是某台主机上 Redis 服务的连接配置（控制端保存，按主机隔离）。
// Password 加密落库，接口里不返回明文。
type RedisConf struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	HostID    uint      `gorm:"uniqueIndex;not null" json:"host_id"`
	Addr      string    `gorm:"size:255" json:"addr"` // host:port
	Username  string    `gorm:"size:128" json:"username"`
	Password  string    `gorm:"type:text" json:"-"`
	DB        int       `gorm:"default:0" json:"db"`
	TLS       bool      `gorm:"default:false" json:"tls"`
	Insecure  bool      `gorm:"default:false" json:"insecure"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MySQLConf 是某台主机上 MySQL 的连接配置（控制端保存，按主机隔离）。
// Password 加密落库，接口里不返回明文。ReadOnly 打开时面板只允许查询类 SQL。
type MySQLConf struct {
	ID       uint   `gorm:"primarykey" json:"id"`
	HostID   uint   `gorm:"uniqueIndex;not null" json:"host_id"`
	Host     string `gorm:"size:255" json:"host"` // 数据库地址，可与所在主机不同
	Port     int    `gorm:"default:3306" json:"port"`
	Username string `gorm:"size:128" json:"username"`
	Password string `gorm:"type:text" json:"-"`
	Database string `gorm:"size:128" json:"database"`
	Charset  string `gorm:"size:32" json:"charset"`
	TLS      bool   `gorm:"default:false" json:"tls"`
	Insecure bool   `gorm:"default:false" json:"insecure"`
	// 这里刻意不加 gorm:"default:true"：GORM 对带默认值的字段在插入时会省略零值，
	// 导致「首次保存时显式选了可写」被数据库默认值改回只读。
	// 「新建连接默认只读」由 handler 负责设置。
	ReadOnly  bool      `json:"read_only"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FileFavorite 是文件管理里的目录收藏，按账号 + 主机范围隔离。
// Scope 形如 "host:1"（本机或纳管主机）或 "ssh:3"（SSH 主机，走 SFTP），
// 两类主机的自增 ID 各自独立，必须加前缀区分。
type FileFavorite struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	UserID    uint      `gorm:"not null;uniqueIndex:idx_fav_owner" json:"user_id"`
	Scope     string    `gorm:"size:64;not null;uniqueIndex:idx_fav_owner" json:"scope"`
	Path      string    `gorm:"size:512;not null;uniqueIndex:idx_fav_owner" json:"path"`
	Name      string    `gorm:"size:255" json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// MySQLBackup 是一个 MySQL 定时备份任务。
//
// 调度跑在控制端：本机主机直接连库导出，远程主机把导出请求转给该主机的 Agent，
// 由 Agent 在其网络位置连库，导出的 SQL 流回控制端落盘。
// 因此任务本身不存密码，凭据仍然只有 MySQLConf 一份，改密码不必回头改任务。
type MySQLBackup struct {
	ID     uint   `gorm:"primarykey" json:"id"`
	HostID uint   `gorm:"index;not null" json:"host_id"`
	Name   string `gorm:"size:128;not null" json:"name"`
	Schema string `gorm:"size:128;not null" json:"schema"`
	// TablesJSON 是选中的表（JSON 数组），空数组表示整库备份
	TablesJSON string `gorm:"column:tables;type:text" json:"-"`

	// 计划：daily（每天）/ weekly（每周几）/ monthly（每月几号），时间用 Hour:Minute
	//
	// 这些字段刻意都不写 gorm:"default:..."：GORM 对带默认值的字段在插入时会省略零值，
	// 于是「00:30 执行」「不压缩」「保留 0 份（不清理）」都会被数据库默认值悄悄改掉。
	// 默认值由 api 层在新建时显式设置。
	Freq     string `gorm:"size:16" json:"freq"`
	Hour     int    `json:"hour"`
	Minute   int    `json:"minute"`
	Weekday  int    `json:"weekday"`   // 0=周日 … 6=周六
	MonthDay int    `json:"month_day"` // 1-28，避免 31 号在小月空跑
	Keep     int    `json:"keep"`      // 保留最近 N 份，0 表示不清理
	Compress bool   `json:"compress"`
	Enabled  bool   `json:"enabled"`

	// 运行态（落库，重启后界面仍有历史结果）
	NextRunAt  *time.Time `json:"next_run_at"`
	LastRunAt  *time.Time `json:"last_run_at"`
	LastStatus string     `gorm:"size:16" json:"last_status"` // ok | failed | running
	LastError  string     `gorm:"type:text" json:"last_error"`
	LastFile   string     `gorm:"size:255" json:"last_file"`
	LastSize   int64      `json:"last_size"`
	LastRows   int64      `json:"last_rows"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`

	// 反序列化后的表列表，仅用于接口输出
	Tables []string `gorm:"-" json:"tables"`
}

// AfterFind 把表列表 JSON 还原成切片，调用方不必关心存储格式。
func (m *MySQLBackup) AfterFind(*gorm.DB) error {
	m.Tables = nil
	raw := strings.TrimSpace(m.TablesJSON)
	if raw == "" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		// 坏数据不该让整个列表接口报错，当作整库处理
		return nil
	}
	m.Tables = list
	return nil
}

// BeforeSave 把表列表序列化进 TablesJSON。
// Tables 为 nil 表示调用方没有改这一项（如局部更新），保持原值不动。
func (m *MySQLBackup) BeforeSave(*gorm.DB) error {
	if m.Tables == nil {
		return nil
	}
	if len(m.Tables) == 0 {
		m.TablesJSON = ""
		return nil
	}
	b, err := json.Marshal(m.Tables)
	if err != nil {
		return err
	}
	m.TablesJSON = string(b)
	return nil
}

// ---------- 存储 ----------

type Store struct {
	DB *gorm.DB
}

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// SQLite 单写者，连接数不宜过大
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(time.Hour)

	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	return s.DB.AutoMigrate(
		&User{}, &AuditLog{}, &Monitor{}, &MonitorResult{}, &SSHHost{}, &Setting{}, &Host{},
		&NacosConf{}, &RedisConf{}, &MySQLConf{}, &FileFavorite{}, &MySQLBackup{},
	)
}

// EnsureLocalHost 保证存在且仅存在一条本机记录。
func (s *Store) EnsureLocalHost() (*Host, error) {
	var h Host
	err := s.DB.Where("kind = ?", "local").First(&h).Error
	if err == nil {
		return &h, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}
	h = Host{Name: "本机", Kind: "local", Enabled: true}
	if err := s.DB.Create(&h).Error; err != nil {
		return nil, err
	}
	return &h, nil
}

// ---------- 通用助手 ----------

func (s *Store) GetSetting(key, def string) string {
	var v Setting
	if err := s.DB.Where("key = ?", key).First(&v).Error; err != nil {
		return def
	}
	if v.Value == "" {
		return def
	}
	return v.Value
}

func (s *Store) SetSetting(key, value string) error {
	return s.DB.Save(&Setting{Key: key, Value: value}).Error
}

func (s *Store) UserCount() (int64, error) {
	var n int64
	err := s.DB.Model(&User{}).Count(&n).Error
	return n, err
}

func (s *Store) GetUserByName(name string) (*User, error) {
	var u User
	if err := s.DB.Where("username = ?", name).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
