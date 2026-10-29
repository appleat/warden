package auth

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"warden/internal/secure"
	"warden/internal/store"
)

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

var roleLevel = map[string]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

func RoleAtLeast(role string, min string) bool {
	return roleLevel[role] >= roleLevel[min]
}

type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

type failEntry struct {
	Count    int
	LastFail time.Time
}

type Manager struct {
	store  *store.Store
	secret []byte
	ttl    time.Duration

	mu    sync.Mutex
	fails map[string]*failEntry
}

func New(st *store.Store, secret string, ttl time.Duration) *Manager {
	return &Manager{store: st, secret: []byte(secret), ttl: ttl, fails: make(map[string]*failEntry)}
}

// Initialized 表示是否已完成首次管理员创建。
func (m *Manager) Initialized() bool {
	n, err := m.store.UserCount()
	return err == nil && n > 0
}

func hashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(b), err
}

func ValidatePassword(p string) error {
	if len(p) < 8 {
		return errors.New("密码至少 8 位")
	}
	if len(p) > 72 {
		return errors.New("密码不能超过 72 位")
	}
	return nil
}

func (m *Manager) Bootstrap(username, password string) (*store.User, error) {
	if m.Initialized() {
		return nil, errors.New("系统已完成初始化")
	}
	if username == "" {
		return nil, errors.New("用户名不能为空")
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	h, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &store.User{Username: username, Password: h, Role: RoleAdmin}
	if err := m.store.DB.Create(u).Error; err != nil {
		return nil, err
	}
	return u, nil
}

func (m *Manager) Issue(u *store.User) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   u.ID,
		Username: u.Username,
		Role:     u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
			Subject:   strconv.FormatUint(uint64(u.ID), 10),
			Issuer:    "warden",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
}

func (m *Manager) Parse(token string) (*Claims, error) {
	c := &Claims{}
	t, err := jwt.ParseWithClaims(token, c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("不支持的签名算法")
		}
		return m.secret, nil
	})
	if err != nil || !t.Valid {
		return nil, errors.New("登录状态已失效，请重新登录")
	}
	return c, nil
}

// ---------- 登录失败限制 ----------

func (m *Manager) key(username, ip string) string { return username + "|" + ip }

func (m *Manager) locked(key string) (bool, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.fails[key]
	if !ok {
		return false, 0
	}
	if e.Count < 5 {
		return false, 0
	}
	remain := 15*time.Minute - time.Since(e.LastFail)
	if remain <= 0 {
		delete(m.fails, key)
		return false, 0
	}
	return true, int(remain.Seconds()) + 1
}

func (m *Manager) recordFail(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.fails[key]
	if !ok || time.Since(e.LastFail) > 15*time.Minute {
		e = &failEntry{}
		m.fails[key] = e
	}
	e.Count++
	e.LastFail = time.Now()
}

func (m *Manager) clearFail(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.fails, key)
}

// Login 校验凭据并签发 token。
func (m *Manager) Login(username, password, totpCode, ip string) (string, *store.User, error) {
	key := m.key(username, ip)
	if locked, remain := m.locked(key); locked {
		return "", nil, fmt.Errorf("失败次数过多，请 %d 秒后重试", remain)
	}

	u, err := m.store.GetUserByName(username)
	if err != nil {
		m.recordFail(key)
		return "", nil, errors.New("用户名或密码错误")
	}
	if u.Disabled {
		return "", nil, errors.New("该账号已被禁用")
	}
	if bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password)) != nil {
		m.recordFail(key)
		return "", nil, errors.New("用户名或密码错误")
	}
	if u.TOTPEnabled {
		if totpCode == "" {
			return "", nil, errors.New("请输入两步验证码")
		}
		if !secure.VerifyTOTP(u.TOTPSecret, totpCode) {
			m.recordFail(key)
			return "", nil, errors.New("两步验证码不正确")
		}
	}

	m.clearFail(key)
	now := time.Now()
	m.store.DB.Model(&store.User{}).Where("id = ?", u.ID).Update("last_login_at", now)
	u.LastLoginAt = &now

	token, err := m.Issue(u)
	return token, u, err
}

func (m *Manager) ChangePassword(uid uint, oldPwd, newPwd string) error {
	var u store.User
	if err := m.store.DB.First(&u, uid).Error; err != nil {
		return errors.New("用户不存在")
	}
	if bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(oldPwd)) != nil {
		return errors.New("原密码不正确")
	}
	if err := ValidatePassword(newPwd); err != nil {
		return err
	}
	h, err := hashPassword(newPwd)
	if err != nil {
		return err
	}
	return m.store.DB.Model(&u).Update("password", h).Error
}

func (m *Manager) SetPassword(uid uint, newPwd string) error {
	if err := ValidatePassword(newPwd); err != nil {
		return err
	}
	h, err := hashPassword(newPwd)
	if err != nil {
		return err
	}
	return m.store.DB.Model(&store.User{}).Where("id = ?", uid).Update("password", h).Error
}

// ---------- 两步验证 ----------

func (m *Manager) EnableTOTP(uid uint, secret string) error {
	return m.store.DB.Model(&store.User{}).Where("id = ?", uid).
		Updates(map[string]any{"totp_secret": secret, "totp_enabled": true}).Error
}

func (m *Manager) DisableTOTP(uid uint) error {
	return m.store.DB.Model(&store.User{}).Where("id = ?", uid).
		Updates(map[string]any{"totp_secret": "", "totp_enabled": false}).Error
}

func (m *Manager) User(uid uint) (*store.User, error) {
	var u store.User
	if err := m.store.DB.First(&u, uid).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
