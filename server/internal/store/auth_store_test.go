package store

// auth 持久化测试：用户保存→重开→LoadAll 恢复（重启不丢账号）。
import (
	"testing"

	"enwords/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthPersistence(t *testing.T) {
	db1, path := openTest(t)
	t.Cleanup(func() { d, _ := db1.DB(); _ = d.Close() })
	as1 := NewAuthStore(db1)

	// 建两个用户并保存
	svc1 := auth.NewService("k", 0)
	_, _ = svc1.CreateUser("dad", "pw12345", "爸爸", auth.RoleAdmin)
	_, _ = svc1.CreateUser("kid", "pw54321", "小明", auth.RoleStudent)
	require.NoError(t, as1.SaveUser(svc1, "dad"))
	require.NoError(t, as1.SaveUser(svc1, "kid"))

	// 重开：新 Service + LoadAll
	db2, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { d, _ := db2.DB(); _ = d.Close() })
	svc2 := auth.NewService("k", 0)
	n, err := NewAuthStore(db2).LoadAll(svc2)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "两个用户恢复")

	// 登录可用（盐哈希还原正确）
	tok, u, err := svc2.Login("kid", "pw54321")
	require.NoError(t, err, "恢复后原密码可登录")
	assert.NotEmpty(t, tok)
	assert.Equal(t, "小明", u.Nickname)
	assert.True(t, svc2.HasAdmin(), "admin 角色恢复")

	// 幂等：重复 LoadAll 不炸
	n2, err := NewAuthStore(db2).LoadAll(svc2)
	require.NoError(t, err)
	assert.Equal(t, 0, n2, "重复恢复跳过")
}
