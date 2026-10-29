package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是服务端的全部可配置项，来自 config.yaml + 环境变量（WARDEN_ 前缀）。
type Config struct {
	// Mode 为 server（控制端）或 agent（被管端）
	Mode        string `yaml:"mode"`
	AgentListen string `yaml:"agent_listen"`
	AgentToken  string `yaml:"agent_token"`
	AgentName   string `yaml:"agent_name"`

	Listen     string `yaml:"listen"`
	DataDir    string `yaml:"data_dir"`
	JWTSecret  string `yaml:"jwt_secret"`
	TokenHours int    `yaml:"token_hours"`
	DockerHost string `yaml:"docker_host"`
	Shell      string `yaml:"shell"`
	FileRoot   string `yaml:"file_root"`
	LogLevel   string `yaml:"log_level"`
	CertFile   string `yaml:"cert_file"`
	KeyFile    string `yaml:"key_file"`

	path string
}

func defaults() Config {
	return Config{
		Mode:        "server",
		AgentListen: ":8090",
		Listen:      ":8080",
		DataDir:    "./data",
		TokenHours: 24,
		DockerHost: "",
		Shell:      "",
		FileRoot:   "/",
		LogLevel:   "info",
	}
}

// Load 读取配置：默认值 -> config.yaml -> 环境变量。
func Load() (*Config, error) {
	cfg := defaults()

	path := "config.yaml"
	if v := os.Getenv("WARDEN_CONFIG"); v != "" {
		path = v
	}
	cfg.path = path

	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return nil, err
		}
	}

	// 环境变量覆盖，便于容器化部署
	envOverride(&cfg.Mode, "WARDEN_MODE")
	envOverride(&cfg.AgentListen, "WARDEN_AGENT_LISTEN")
	envOverride(&cfg.AgentToken, "WARDEN_AGENT_TOKEN")
	envOverride(&cfg.AgentName, "WARDEN_AGENT_NAME")
	envOverride(&cfg.Listen, "WARDEN_LISTEN")
	envOverride(&cfg.DataDir, "WARDEN_DATA_DIR")
	envOverride(&cfg.DockerHost, "WARDEN_DOCKER_HOST")
	envOverride(&cfg.Shell, "WARDEN_SHELL")
	envOverride(&cfg.FileRoot, "WARDEN_FILE_ROOT")
	envOverride(&cfg.LogLevel, "WARDEN_LOG_LEVEL")
	envOverride(&cfg.CertFile, "WARDEN_CERT_FILE")
	envOverride(&cfg.KeyFile, "WARDEN_KEY_FILE")
	envOverride(&cfg.JWTSecret, "WARDEN_JWT_SECRET")
	if v := os.Getenv("WARDEN_TOKEN_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.TokenHours = n
		}
	}

	if cfg.TokenHours <= 0 {
		cfg.TokenHours = 24
	}
	if cfg.FileRoot == "" {
		cfg.FileRoot = "/"
	}
	if cfg.AgentListen == "" {
		cfg.AgentListen = ":8090"
	}

	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if cfg.Mode != "agent" {
		cfg.Mode = "server"
	}

	// Agent 模式不落盘、不需要数据库与 JWT 密钥
	if cfg.Mode == "server" {
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			return nil, err
		}
		if cfg.JWTSecret == "" {
			secret, err := loadOrCreateSecret(filepath.Join(cfg.DataDir, "jwt.key"))
			if err != nil {
				return nil, err
			}
			cfg.JWTSecret = secret
		}
	}

	return &cfg, nil
}

func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "warden.db")
}

// SecretKey 用于加密敏感字段（SSH 密码等），由 JWT 密钥派生。
func (c *Config) SecretKey() []byte {
	sum := strings.Repeat(c.JWTSecret, 32)
	return []byte(sum[:32])
}

func envOverride(dst *string, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func loadOrCreateSecret(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	s := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		return "", err
	}
	return s, nil
}

// TokenTTL 返回 token 有效期。
func (c *Config) TokenTTL() time.Duration {
	return time.Duration(c.TokenHours) * time.Hour
}
