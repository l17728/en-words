package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTokenCannotBeReassignedToAnotherUser(t *testing.T) {
	s := newAuth()
	alice, err := s.CreateUser("alice", "alice-password", "Alice", RoleStudent)
	require.NoError(t, err)
	bob, err := s.CreateUser("bob", "bob-password", "Bob", RoleAdmin)
	require.NoError(t, err)
	token, _, err := s.Login(alice.Username, "alice-password")
	require.NoError(t, err)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims Claims
	require.NoError(t, json.Unmarshal(payload, &claims))
	claims.UID, claims.Username, claims.Role = bob.ID, bob.Username, bob.Role
	forgedPayload, err := json.Marshal(claims)
	require.NoError(t, err)
	parts[1] = base64.RawURLEncoding.EncodeToString(forgedPayload)
	_, err = s.Verify(strings.Join(parts, "."))
	require.ErrorIs(t, err, ErrBadToken)

	verified, err := s.Verify(token)
	require.NoError(t, err)
	require.Equal(t, alice.ID, verified.UID)
	require.Equal(t, RoleStudent, verified.Role)
	_, err = NewService("other-secret", time.Hour).Verify(token)
	require.ErrorIs(t, err, ErrBadToken)
}

func TestDisabledUserCannotLogInUntilEnabled(t *testing.T) {
	s := newAuth()
	_, err := s.CreateUser("student", "correct-password", "Student", RoleStudent)
	require.NoError(t, err)
	require.NoError(t, s.Disable("student"))
	_, _, err = s.Login("student", "correct-password")
	require.ErrorIs(t, err, ErrDisabled)
	_, _, err = s.Login("student", "wrong-password")
	require.ErrorIs(t, err, ErrBadCredentials)
	require.NoError(t, s.Enable("student"))
	_, _, err = s.Login("student", "correct-password")
	require.NoError(t, err)
	require.Error(t, s.Disable("missing"))
}

func TestRestoredUserKeepsCredentialsAndNextID(t *testing.T) {
	before := newAuth()
	u, err := before.CreateUser("restored", "persisted-password", "Old name", RoleStudent)
	require.NoError(t, err)
	after := newAuth()
	require.NoError(t, after.RestoreUser(42, u.Username, u.Nickname, u.Role, u.Status, u.SaltForStore(), u.HashForStore()))
	require.True(t, after.HasUser("restored"))
	restored, err := after.GetUserByID(42)
	require.NoError(t, err)
	require.Equal(t, "Old name", restored.Nickname)
	_, _, err = after.Login("restored", "persisted-password")
	require.NoError(t, err)
	_, _, err = after.Login("restored", "wrong-password")
	require.ErrorIs(t, err, ErrBadCredentials)

	// Replaying a persisted row must not overwrite an already loaded account.
	require.NoError(t, after.RestoreUser(42, "restored", "Corrupt", RoleAdmin, "disabled", "bad", "bad"))
	still, err := after.GetUser("restored")
	require.NoError(t, err)
	require.Equal(t, RoleStudent, still.Role)
	require.Equal(t, "Old name", still.Nickname)
	next, err := after.CreateUser("new", "new-password", "New", RoleStudent)
	require.NoError(t, err)
	require.Equal(t, int64(43), next.ID)
}

func TestUserCopiesAndListDoNotExposeCredentials(t *testing.T) {
	s := newAuth()
	u, err := s.CreateUser("student", "password", "Original", RoleStudent)
	require.NoError(t, err)
	require.Error(t, s.UpdateNickname("student", "bad??name"))
	require.Error(t, s.UpdateNickname("missing", "Valid"))
	require.NoError(t, s.UpdateNickname("student", "Updated"))

	listed := s.ListUsers()
	require.Len(t, listed, 1)
	require.Equal(t, "Updated", listed[0].Nickname)
	require.Empty(t, listed[0].SaltForStore())
	require.Empty(t, listed[0].HashForStore())
	listed[0].Nickname = "Injected"
	u.Nickname = "Injected"
	got, err := s.GetUser("student")
	require.NoError(t, err)
	require.Equal(t, "Updated", got.Nickname)
	require.NotEmpty(t, got.HashForStore())
	require.False(t, s.HasUser("missing"))
}

func TestServiceTokenUsesExistingAccountIdentity(t *testing.T) {
	s := newAuth()
	_, err := s.SignService("missing")
	require.Error(t, err)
	s.EnsureAgentAccounts()
	token, err := s.SignService("teacher-agent")
	require.NoError(t, err)
	claims, err := s.Verify(token)
	require.NoError(t, err)
	require.Equal(t, "teacher-agent", claims.Username)
	require.Equal(t, RoleTeacherAgent, claims.Role)
}
