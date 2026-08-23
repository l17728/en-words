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
