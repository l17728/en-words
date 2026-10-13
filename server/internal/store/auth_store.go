// auth 域 SQLite 存取（SDD §9 users 表；auth.Service 保持内存态，本层提供持久化同步）。
package store

import (
	"enwords/internal/auth"

	"gorm.io/gorm"
)

// AuthStore 用户持久化（auth.Service 的可选后端：登录后快照落盘+启动加载）。
type AuthStore struct{ db *gorm.DB }

func NewAuthStore(db *gorm.DB) *AuthStore { return &AuthStore{db: db} }

// SaveUser 保存/更新用户（盐+哈希）。
func (a *AuthStore) SaveUser(svc *auth.Service, username string) error {
	u, err := svc.GetUser(username)
	if err != nil {
		return err
	}
	return a.db.Save(&User{ID: u.ID, Username: u.Username, Nickname: u.Nickname,
		Role: string(u.Role), Status: u.Status,
		Salt: u.SaltForStore(), Hash: u.HashForStore()}).Error
}

// LoadAll 启动时把全部用户装载回 auth.Service（重启不丢账号）。
// 返回新加载数量（已存在的跳过不计数）。
func (a *AuthStore) LoadAll(svc *auth.Service) (int, error) {
	var us []User
	if err := a.db.Order("id").Find(&us).Error; err != nil {
		return 0, err
	}
	n := 0
	for _, u := range us {
		if svc.HasUser(u.Username) {
			continue // 幂等：已存在不计数
		}
		if err := svc.RestoreUser(u.ID, u.Username, u.Nickname, auth.Role(u.Role),
			u.Status, u.Salt, u.Hash); err == nil {
			n++
		}
	}
	return n, nil
}
