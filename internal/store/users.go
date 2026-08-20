package store

import (
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"strings"
	"time"
)

const (
	UserRoleSuperadmin = "superadmin"
	UserRoleUser       = "user"
)

type AppUser struct {
	ID                int64      `json:"id"`
	Username          string     `json:"username"`
	Email             string     `json:"email"`
	PasswordHash      string     `json:"-"`
	Role              string     `json:"role"`
	Enabled           bool       `json:"enabled"`
	AuthSource        string     `json:"auth_source"`
	OIDCIssuer        string     `json:"-"`
	OIDCSubject       string     `json:"-"`
	MCPTokenHash      string     `json:"-"`
	MCPTokenCreatedAt *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastLoginAt       *time.Time `json:"last_login_at,omitempty"`
}

type CreateUserInput struct {
	Username     string
	Email        string
	PasswordHash string
	Role         string
	Enabled      bool
	AuthSource   string
	OIDCIssuer   string
	OIDCSubject  string
}

type UpdateUserInput struct {
	Username     string
	Email        string
	PasswordHash *string
	Role         string
	Enabled      bool
}

type OIDCLoginState struct {
	Nonce        string
	CodeVerifier string
	ReturnTo     string
	ExpiresAt    time.Time
}

func normalizeAccountValue(value string) string { return strings.TrimSpace(value) }

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || !strings.Contains(value, "@") {
		return "", errors.New("invalid email address")
	}
	return value, nil
}

func validUserRole(role string) bool { return role == UserRoleSuperadmin || role == UserRoleUser }

func scanAppUser(scanner interface{ Scan(...any) error }) (AppUser, error) {
	var user AppUser
	var enabled int
	var created, updated string
	var lastLogin, mcpTokenCreated sql.NullString
	err := scanner.Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.Role, &enabled, &user.AuthSource, &user.OIDCIssuer, &user.OIDCSubject, &created, &updated, &lastLogin, &user.MCPTokenHash, &mcpTokenCreated)
	if err != nil {
		return AppUser{}, err
	}
	user.Enabled = enabled != 0
	if mcpTokenCreated.Valid {
		value, _ := time.Parse(time.RFC3339Nano, mcpTokenCreated.String)
		user.MCPTokenCreatedAt = &value
	}
	user.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	user.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if lastLogin.Valid {
		value, _ := time.Parse(time.RFC3339Nano, lastLogin.String)
		user.LastLoginAt = &value
	}
	return user, nil
}

const selectAppUser = `SELECT id,username,email,password_hash,role,enabled,auth_source,oidc_issuer,oidc_subject,created_at,updated_at,last_login_at,mcp_token_hash,mcp_token_created_at FROM app_user`

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_user`).Scan(&count)
	return count, err
}

func (s *Store) CreateUser(ctx context.Context, input CreateUserInput) (AppUser, error) {
	input.Username = normalizeAccountValue(input.Username)
	var err error
	input.Email, err = normalizeEmail(input.Email)
	if err != nil || input.Username == "" || !validUserRole(input.Role) {
		return AppUser{}, errors.New("invalid user account")
	}
	if input.AuthSource == "" {
		input.AuthSource = "local"
	}
	if input.AuthSource != "local" && input.AuthSource != "oidc" {
		return AppUser{}, errors.New("invalid authentication source")
	}
	nowValue := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT INTO app_user(username,email,password_hash,role,enabled,auth_source,oidc_issuer,oidc_subject,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, input.Username, input.Email, input.PasswordHash, input.Role, boolInt(input.Enabled), input.AuthSource, input.OIDCIssuer, input.OIDCSubject, nowValue, nowValue)
	if err != nil {
		return AppUser{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AppUser{}, err
	}
	return s.GetUserByID(ctx, id)
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (AppUser, error) {
	user, err := scanAppUser(s.db.QueryRowContext(ctx, selectAppUser+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AppUser{}, sql.ErrNoRows
	}
	return user, err
}

func (s *Store) GetUserByIdentifier(ctx context.Context, identifier string) (AppUser, error) {
	identifier = normalizeAccountValue(identifier)
	return scanAppUser(s.db.QueryRowContext(ctx, selectAppUser+` WHERE username=? COLLATE NOCASE OR email=? COLLATE NOCASE LIMIT 1`, identifier, identifier))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (AppUser, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return AppUser{}, err
	}
	return scanAppUser(s.db.QueryRowContext(ctx, selectAppUser+` WHERE email=? COLLATE NOCASE LIMIT 1`, email))
}

func (s *Store) GetUserByOIDC(ctx context.Context, issuer, subject string) (AppUser, error) {
	return scanAppUser(s.db.QueryRowContext(ctx, selectAppUser+` WHERE oidc_issuer=? AND oidc_subject=? LIMIT 1`, issuer, subject))
}

func (s *Store) ListUsers(ctx context.Context) ([]AppUser, error) {
	rows, err := s.db.QueryContext(ctx, selectAppUser+` ORDER BY role='superadmin' DESC, username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AppUser, 0)
	for rows.Next() {
		user, scanErr := scanAppUser(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, user)
	}
	return result, rows.Err()
}

func (s *Store) UpdateUser(ctx context.Context, id int64, input UpdateUserInput) (AppUser, error) {
	input.Username = normalizeAccountValue(input.Username)
	var err error
	input.Email, err = normalizeEmail(input.Email)
	if err != nil || id <= 0 || input.Username == "" || !validUserRole(input.Role) {
		return AppUser{}, errors.New("invalid user account")
	}
	if input.PasswordHash != nil {
		_, err = s.db.ExecContext(ctx, `UPDATE app_user SET username=?,email=?,password_hash=?,role=?,enabled=?,updated_at=? WHERE id=?`, input.Username, input.Email, *input.PasswordHash, input.Role, boolInt(input.Enabled), time.Now().UTC().Format(time.RFC3339Nano), id)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE app_user SET username=?,email=?,role=?,enabled=?,updated_at=? WHERE id=?`, input.Username, input.Email, input.Role, boolInt(input.Enabled), time.Now().UTC().Format(time.RFC3339Nano), id)
	}
	if err != nil {
		return AppUser{}, err
	}
	return s.GetUserByID(ctx, id)
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	user, err := s.GetUserByID(ctx, id)
	if err != nil {
		return err
	}
	if user.Role == UserRoleSuperadmin {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_user WHERE role=? AND enabled=1`, UserRoleSuperadmin).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return errors.New("cannot delete the last enabled superadmin")
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM app_user WHERE id=?`, id)
	return err
}

func (s *Store) UpsertOIDCUser(ctx context.Context, issuer, subject, username, email string) (AppUser, error) {
	if user, err := s.GetUserByOIDC(ctx, issuer, subject); err == nil {
		_, updateErr := s.db.ExecContext(ctx, `UPDATE app_user SET username=?,email=?,enabled=1,updated_at=? WHERE id=?`, username, strings.ToLower(email), time.Now().UTC().Format(time.RFC3339Nano), user.ID)
		if updateErr != nil {
			return AppUser{}, updateErr
		}
		return s.GetUserByID(ctx, user.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppUser{}, err
	}
	if existing, err := s.GetUserByEmail(ctx, email); err == nil {
		_, linkErr := s.db.ExecContext(ctx, `UPDATE app_user SET oidc_issuer=?,oidc_subject=?,updated_at=? WHERE id=?`, issuer, subject, time.Now().UTC().Format(time.RFC3339Nano), existing.ID)
		if linkErr != nil {
			return AppUser{}, linkErr
		}
		return s.GetUserByID(ctx, existing.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppUser{}, err
	}
	return s.CreateUser(ctx, CreateUserInput{Username: username, Email: email, Role: UserRoleUser, Enabled: true, AuthSource: "oidc", OIDCIssuer: issuer, OIDCSubject: subject})
}

func (s *Store) TouchUserLogin(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE app_user SET last_login_at=?,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) CreateUserSession(ctx context.Context, tokenHash string, userID int64, expiresAt time.Time) error {
	nowValue := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_session(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`, tokenHash, userID, expiresAt.UTC().Format(time.RFC3339Nano), nowValue)
	return err
}

func (s *Store) UserBySession(ctx context.Context, tokenHash string, nowValue time.Time) (AppUser, error) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM user_session WHERE expires_at<=?`, nowValue.UTC().Format(time.RFC3339Nano))
	const selectSessionUser = `SELECT app_user.id,app_user.username,app_user.email,app_user.password_hash,app_user.role,app_user.enabled,app_user.auth_source,app_user.oidc_issuer,app_user.oidc_subject,app_user.created_at,app_user.updated_at,app_user.last_login_at,app_user.mcp_token_hash,app_user.mcp_token_created_at FROM app_user`
	return scanAppUser(s.db.QueryRowContext(ctx, selectSessionUser+` JOIN user_session ON user_session.user_id=app_user.id WHERE user_session.token_hash=? AND user_session.expires_at>? AND app_user.enabled=1`, tokenHash, nowValue.UTC().Format(time.RFC3339Nano)))
}

func (s *Store) DeleteUserSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_session WHERE token_hash=?`, tokenHash)
	return err
}

func (s *Store) SaveOIDCState(ctx context.Context, stateHash string, value OIDCLoginState) error {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM oidc_login_state WHERE expires_at<=?`, time.Now().UTC().Format(time.RFC3339Nano))
	_, err := s.db.ExecContext(ctx, `INSERT INTO oidc_login_state(state_hash,nonce,code_verifier,return_to,expires_at,created_at) VALUES(?,?,?,?,?,?)`, stateHash, value.Nonce, value.CodeVerifier, value.ReturnTo, value.ExpiresAt.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ConsumeOIDCState(ctx context.Context, stateHash string, nowValue time.Time) (OIDCLoginState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OIDCLoginState{}, err
	}
	defer tx.Rollback()
	var result OIDCLoginState
	var expires string
	if err := tx.QueryRowContext(ctx, `SELECT nonce,code_verifier,return_to,expires_at FROM oidc_login_state WHERE state_hash=? AND expires_at>?`, stateHash, nowValue.UTC().Format(time.RFC3339Nano)).Scan(&result.Nonce, &result.CodeVerifier, &result.ReturnTo, &expires); err != nil {
		return OIDCLoginState{}, err
	}
	result.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_login_state WHERE state_hash=?`, stateHash); err != nil {
		return OIDCLoginState{}, err
	}
	if err := tx.Commit(); err != nil {
		return OIDCLoginState{}, err
	}
	return result, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) SetMCPToken(ctx context.Context, userID int64, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE app_user SET mcp_token_hash=?,mcp_token_created_at=?,updated_at=? WHERE id=?`, tokenHash, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), userID)
	return err
}

func (s *Store) UserByMCPToken(ctx context.Context, tokenHash string) (AppUser, error) {
	const query = `SELECT id,username,email,password_hash,role,enabled,auth_source,oidc_issuer,oidc_subject,created_at,updated_at,last_login_at,mcp_token_hash,mcp_token_created_at FROM app_user WHERE mcp_token_hash=? AND enabled=1`
	return scanAppUser(s.db.QueryRowContext(ctx, query, tokenHash))
}
