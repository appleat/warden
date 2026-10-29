package store

import (
	"fmt"
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
		&NacosConf{}, &RedisConf{},
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
