package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestUserLifecycleAndHashedSession(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin, err := st.CreateUser(ctx, CreateUserInput{Username: "admin", Email: "Admin@Example.com", PasswordHash: "hash", Role: UserRoleSuperadmin, Enabled: true, AuthSource: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if admin.Email != "admin@example.com" || admin.Role != UserRoleSuperadmin {
		t.Fatalf("admin = %#v", admin)
	}
	if _, err := st.CreateUser(ctx, CreateUserInput{Username: "invalid", Email: "not-an-email", PasswordHash: "hash", Role: UserRoleUser, Enabled: true, AuthSource: "local"}); err == nil {
		t.Fatal("invalid email must be rejected")
	}
	if err := st.CreateUserSession(ctx, "token-hash", admin.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	bySession, err := st.UserBySession(ctx, "token-hash", time.Now())
	if err != nil || bySession.ID != admin.ID {
		t.Fatalf("session user = %#v, err = %v", bySession, err)
	}
	if err := st.DeleteUser(ctx, admin.ID); err == nil {
		t.Fatal("last superadmin deletion must fail")
	}
	if _, err := st.CreateUser(ctx, CreateUserInput{Username: "second", Email: "second@example.com", PasswordHash: "hash", Role: UserRoleSuperadmin, Enabled: true, AuthSource: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetUserByID(ctx, admin.ID); err != sql.ErrNoRows {
		t.Fatalf("deleted lookup error = %v", err)
	}
}

func TestOIDCUserLinksExistingEmailAndStateIsSingleUse(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "oidc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	local, err := st.CreateUser(ctx, CreateUserInput{Username: "alice", Email: "alice@example.com", PasswordHash: "hash", Role: UserRoleUser, Enabled: true, AuthSource: "local"})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := st.UpsertOIDCUser(ctx, "https://issuer", "subject-1", "alice-oidc", "alice@example.com")
	if err != nil || linked.ID != local.ID || linked.OIDCSubject != "subject-1" {
		t.Fatalf("linked user = %#v, err = %v", linked, err)
	}
	expires := time.Now().Add(time.Minute)
	if err := st.SaveOIDCState(ctx, "state-hash", OIDCLoginState{Nonce: "nonce", CodeVerifier: "verifier", ReturnTo: "/quality", ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	state, err := st.ConsumeOIDCState(ctx, "state-hash", time.Now())
	if err != nil || state.Nonce != "nonce" || state.ReturnTo != "/quality" {
		t.Fatalf("state = %#v, err = %v", state, err)
	}
	if _, err := st.ConsumeOIDCState(ctx, "state-hash", time.Now()); err == nil {
		t.Fatal("OIDC state replay must fail")
	}
}
