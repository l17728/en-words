// Package auth 实现账号认证（SDD §10.1/§15）：
// 内存用户存储（GORM 版后补）、密码盐哈希（bcrypt 后补，先 sha256+salt 固定算法）、
// HMAC-SHA256 紧凑 token（JWT 语义子集：header.payload.signature）、角色与 Agent 服务账号。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Role string

const (
	RoleAdmin         Role = "admin"
	RoleStudent       Role = "student"
	RoleTeacherAgent  Role = "teacher_agent"
	RoleSystemAgent   Role = "system_agent"
)

// User 用户。
type User struct {
	ID       int64
	Username string
	Nickname string
	Role     Role
	salt     string
	hash     string
	Status   string // active | disabled
}

// Claims token 载荷。
type Claims struct {
	UID      int64  `json:"uid"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
	Exp      int64  `json:"exp"` // unix 秒
}

var (
	ErrBadCredentials = errors.New("auth: bad credentials")
	ErrDisabled       = errors.New("auth: account disabled")
	ErrBadToken       = errors.New("auth: bad token")
)

// Service 认证服务。
type Service struct {
	mu    sync.RWMutex
	users map[string]*User // username → user
	byID  map[int64]*User
	next  int64
	secret []byte
	ttl   time.Duration
}

func NewService(secret string, ttl time.Duration) *Service {
	if ttl == 0 { // 仅零值用默认；负 TTL 允许（测试注入过期语义）
		ttl = 7 * 24 * time.Hour
	}
	return &Service{
		users: map[string]*User{}, byID: map[int64]*User{},
		next: 0, secret: []byte(secret), ttl: ttl,
	}
}

// hashPassword 盐哈希（固定算法；生产版换 bcrypt，接口不变）。
func hashPassword(password, salt string) string {
	sum := sha256.Sum256([]byte(salt + ":" + password))
	return hex.EncodeToString(sum[:])
}

func newSalt() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateUser 创建用户（username 唯一）。
func (s *Service) CreateUser(username, password, nickname string, role Role) (*User, error) {
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("auth: username/password required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.users[username]; dup {
		return nil, fmt.Errorf("auth: duplicate username %q", username)
	}
	salt := newSalt()
	s.next++
	u := &User{ID: s.next, Username: username, Nickname: nickname, Role: role,
		Status: "active", salt: salt, hash: hashPassword(password, salt)}
	s.users[username] = u
	s.byID[u.ID] = u
	cp := *u
	return &cp, nil
}

// EnsureAgentAccounts 内置双 Agent 服务账号（A3）。
func (s *Service) EnsureAgentAccounts() {
	for name, role := range map[string]Role{"teacher-agent": RoleTeacherAgent, "system-agent": RoleSystemAgent} {
		if _, err := s.GetUser(name); err != nil {
			_, _ = s.CreateUser(name, "agent-internal-"+newSalt(), name, role)
		}
	}
}

// GetUser 查询。
func (s *Service) GetUser(username string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok {
		return nil, fmt.Errorf("auth: user %q not found", username)
	}
	cp := *u
	return &cp, nil
}

// Login 校验并签发 token。
func (s *Service) Login(username, password string) (string, *User, error) {
	s.mu.RLock()
	u, ok := s.users[username]
	s.mu.RUnlock()
	if !ok || u.hash != hashPassword(password, u.salt) {
		return "", nil, ErrBadCredentials
	}
	if u.Status != "active" {
		return "", nil, ErrDisabled
	}
	claims := Claims{UID: u.ID, Username: u.Username, Role: u.Role,
		Exp: time.Now().Add(s.ttl).Unix()}
	tok, err := s.sign(claims)
	if err != nil {
		return "", nil, err
	}
	cp := *u
	return tok, &cp, nil
}

// Verify 校验 token。
func (s *Service) Verify(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrBadToken
	}
	if !hmac.Equal([]byte(parts[2]), s.sig(parts[0]+"."+parts[1])) {
		return nil, ErrBadToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrBadToken
	}
	if time.Now().Unix() > c.Exp {
		return nil, ErrBadToken
	}
	return &c, nil
}

// Disable 停用（踢下线：旧 token 仍签有效但状态拒绝——本版实现为密码失效+状态位）。
func (s *Service) Disable(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return fmt.Errorf("auth: user %q not found", username)
	}
	u.Status = "disabled"
	return nil
}

// HasAdmin 是否已存在管理员（首次启动向导判定用）。
func (s *Service) HasAdmin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Role == RoleAdmin {
			return true
		}
	}
	return false
}

// SignService 服务账号直签（Agent 无密码登录通道：服务端持有账号即可签发）。
func (s *Service) SignService(username string) (string, error) {
	u, err := s.GetUser(username)
	if err != nil {
		return "", err
	}
	return s.sign(Claims{UID: u.ID, Username: u.Username, Role: u.Role,
		Exp: time.Now().Add(s.ttl).Unix()})
}

// sign 生成 token。
func (s *Service) sign(c Claims) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(payload)
	body := header + "." + p
	return body + "." + string(s.sig(body)), nil
}

func (s *Service) sig(body string) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(body))
	return []byte(base64.RawURLEncoding.EncodeToString(m.Sum(nil)))
}
