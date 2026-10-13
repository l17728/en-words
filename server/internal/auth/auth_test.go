package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAuth() *Service { return NewService("test-secret", time.Hour) }

func TestCreateLoginVerify(t *testing.T) {
	s := newAuth()
	u, err := s.CreateUser("stu1", "pw123", "小明", RoleStudent)
	require.NoError(t, err)
	assert.NotZero(t, u.ID)
	assert.Equal(t, RoleStudent, u.Role)

	tok, lu, err := s.Login("stu1", "pw123")
	require.NoError(t, err)
	assert.Equal(t, "小明", lu.Nickname)

	claims, err := s.Verify(tok)
	require.NoError(t, err)
	assert.Equal(t, int64(u.ID), claims.UID)
	assert.Equal(t, RoleStudent, claims.Role)
}

func TestBadCredentialsAndDup(t *testing.T) {
	s := newAuth()
	_, _ = s.CreateUser("a", "pw", "A", RoleAdmin)
	_, _, err := s.Login("a", "wrong")
	assert.ErrorIs(t, err, ErrBadCredentials)
	_, _, err = s.Login("ghost", "pw")
	assert.ErrorIs(t, err, ErrBadCredentials)
	_, err = s.CreateUser("a", "x", "", RoleStudent)
	assert.Error(t, err, "重复用户名拒绝")
	_, err = s.CreateUser("", "x", "", RoleStudent)
	assert.Error(t, err)
}

func TestTokenExpiry(t *testing.T) {
	s := NewService("sec", -time.Second) // 已过期 TTL
	_, _ = s.CreateUser("b", "pw", "", RoleStudent)
	tok, _, err := s.Login("b", "pw")
	require.NoError(t, err)
	_, err = s.Verify(tok)
	assert.ErrorIs(t, err, ErrBadToken)
}

func TestTamperedToken(t *testing.T) {
	s := newAuth()
	_, _ = s.CreateUser("c", "pw", "", RoleAdmin)
	tok, _, _ := s.Login("c", "pw")
	bad := tok + "x"
	_, err := s.Verify(bad)
	assert.ErrorIs(t, err, ErrBadToken)
	_, err = s.Verify("not.a.token")
	assert.ErrorIs(t, err, ErrBadToken)
}

func TestDisable(t *testing.T) {
	s := newAuth()
	_, _ = s.CreateUser("d", "pw", "", RoleStudent)
	require.NoError(t, s.Disable("d"))
	_, _, err := s.Login("d", "pw")
	assert.ErrorIs(t, err, ErrDisabled)
}

func TestAgentAccounts(t *testing.T) {
	s := newAuth()
	s.EnsureAgentAccounts()
	s.EnsureAgentAccounts() // 幂等
	u, err := s.GetUser("teacher-agent")
	require.NoError(t, err)
	assert.Equal(t, RoleTeacherAgent, u.Role)
	u2, err := s.GetUser("system-agent")
	require.NoError(t, err)
	assert.Equal(t, RoleSystemAgent, u2.Role)
}

func TestHasAdmin(t *testing.T) {
	s := newAuth()
	assert.False(t, s.HasAdmin(), "空库无管理员")
	_, _ = s.CreateUser("stu", "pw123", "", RoleStudent)
	assert.False(t, s.HasAdmin(), "学生不算管理员")
	_, _ = s.CreateUser("dad", "family123", "爸爸", RoleAdmin)
	assert.True(t, s.HasAdmin())
}

func TestUserManagement(t *testing.T) {
	s := newAuth()
	_, _ = s.CreateUser("u1", "oldpw12", "", RoleStudent)

	// GetUserByID
	u, err := s.GetUserByID(1)
	require.NoError(t, err)
	assert.Equal(t, "u1", u.Username)
	_, err = s.GetUserByID(999)
	assert.Error(t, err)

	// ChangePassword：旧密码错拒；成功后新密码可登录
	assert.ErrorIs(t, s.ChangePassword("u1", "wrong", "newpw34"), ErrBadCredentials)
	assert.NoError(t, s.ChangePassword("u1", "oldpw12", "newpw34"))
	tok, _, err := s.Login("u1", "newpw34")
	require.NoError(t, err)
	require.NotEmpty(t, tok)
	assert.Error(t, s.ChangePassword("u1", "newpw34", "12"), "新密码过短拒绝")

	// ResetPassword（管理员）：不需旧密码；顺带解除停用
	require.NoError(t, s.Disable("u1"))
	_, _, err = s.Login("u1", "newpw34")
	assert.ErrorIs(t, err, ErrDisabled)
	require.NoError(t, s.ResetPassword("u1", "resetpw9"))
	tok2, lu, err := s.Login("u1", "resetpw9")
	require.NoError(t, err, "重置后自动解封可登录")
	require.NotEmpty(t, tok2)
	assert.Equal(t, "active", lu.Status)
	assert.Error(t, s.ResetPassword("ghost", "x12345"), "不存在用户拒绝")
	assert.Error(t, s.ResetPassword("u1", "123"), "过短拒绝")

	// Enable
	require.NoError(t, s.Disable("u1"))
	require.NoError(t, s.Enable("u1"))
	_, lu2, err := s.Login("u1", "resetpw9")
	require.NoError(t, err)
	assert.Equal(t, "active", lu2.Status)
	assert.Error(t, s.Enable("ghost"))
}
