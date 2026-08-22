package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/config"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
)

const authSessionCookie = "ocr_session"

type authContextKey string

const authUserKey authContextKey = "auth-user"

type permissionCacheEntry struct {
	Allowed   bool
	ExpiresAt time.Time
}

type gitLabIdentityCacheEntry struct {
	UserID    int64
	ExpiresAt time.Time
}

type authManager struct {
	store        *store.Store
	gitlab       *gitlab.Client
	cfg          config.Config
	initErr      error
	permissionMu sync.Mutex
	permissions  map[string]permissionCacheEntry
	identities   map[int64]gitLabIdentityCacheEntry
}

type loginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

type setupRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userMutationRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password,omitempty"`
	Role     string `json:"role"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

type oidcClaims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     *bool  `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	Nonce             string `json:"nonce"`
}

func newAuthManager(st *store.Store, gl *gitlab.Client, cfg config.Config) *authManager {
	manager := &authManager{store: st, gitlab: gl, cfg: cfg, permissions: make(map[string]permissionCacheEntry), identities: make(map[int64]gitLabIdentityCacheEntry)}
	if cfg.Auth.Enabled {
		manager.initErr = manager.bootstrapAdmin(context.Background())
	}
	return manager
}

func (a *authManager) bootstrapAdmin(ctx context.Context) error {
	count, err := a.store.CountUsers(ctx)
	if err != nil || count > 0 {
		return err
	}
	bootstrap := a.cfg.Auth.BootstrapAdmin
	if strings.TrimSpace(bootstrap.Username) == "" || strings.TrimSpace(bootstrap.Email) == "" || bootstrap.Password == "" {
		return nil
	}
	hash, err := hashPassword(bootstrap.Password)
	if err != nil {
		return err
	}
	_, err = a.store.CreateUser(ctx, store.CreateUserInput{Username: bootstrap.Username, Email: bootstrap.Email, PasswordHash: hash, Role: store.UserRoleSuperadmin, Enabled: true, AuthSource: "local"})
	return err
}

func hashPassword(password string) (string, error) {
	if len(password) < 10 {
		return "", errors.New("password must contain at least 10 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (a *authManager) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/auth/config", a.handleConfig)
	mux.HandleFunc("/api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("/api/v1/admin/config", a.handleAdminConfig)
	mux.HandleFunc("/api/v1/auth/mcp-config", a.handleMCPConfig)
	mux.HandleFunc("/api/v1/auth/setup", a.handleSetup)
	mux.HandleFunc("/api/v1/auth/logout", a.handleLogout)
	mux.HandleFunc("/api/v1/auth/oidc/login", a.handleOIDCLogin)
	mux.HandleFunc("/api/v1/auth/oidc/callback", a.handleOIDCCallback)
	mux.HandleFunc("/api/v1/admin/users", a.handleUsers)
	mux.HandleFunc("/api/v1/admin/users/", a.handleUser)
}

func (a *authManager) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	count, err := a.store.CountUsers(r.Context())
	setupRequired := a.cfg.Auth.Enabled && err == nil && count == 0
	writeJSON(w, map[string]any{"enabled": a.cfg.Auth.Enabled, "setup_required": setupRequired, "oidc_enabled": a.cfg.Auth.Enabled && !setupRequired && a.cfg.Auth.OIDC.Enabled, "oidc_label": "OIDC 登录"}, err)
}

func (a *authManager) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !a.cfg.Auth.Enabled || a.initErr != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "authentication_unavailable", a.initErr)
		return
	}
	count, err := a.store.CountUsers(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "user_count_failed", err)
		return
	}
	if count != 0 {
		writeAdminError(w, http.StatusConflict, "setup_already_completed", nil)
		return
	}
	var request setupRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_setup_body", err)
		return
	}
	hash, err := hashPassword(request.Password)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_password", err)
		return
	}
	user, err := a.store.CreateUser(r.Context(), store.CreateUserInput{Username: request.Username, Email: request.Email, PasswordHash: hash, Role: store.UserRoleSuperadmin, Enabled: true, AuthSource: "local"})
	if err != nil {
		writeAdminError(w, http.StatusConflict, "setup_failed", err)
		return
	}
	if err := a.startSession(w, r, user); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "session_create_failed", err)
		return
	}
	writeJSON(w, publicUser(user), nil)
}

func (a *authManager) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !a.cfg.Auth.Enabled || a.initErr != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "authentication_unavailable", a.initErr)
		return
	}
	var request loginRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_login_body", err)
		return
	}
	user, err := a.store.GetUserByIdentifier(r.Context(), request.Identifier)
	if err != nil || !user.Enabled || user.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.Password)) != nil {
		writeAdminError(w, http.StatusUnauthorized, "invalid_credentials", nil)
		return
	}
	if err := a.startSession(w, r, user); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "session_create_failed", err)
		return
	}
	writeJSON(w, publicUser(user), nil)
}

func (a *authManager) startSession(w http.ResponseWriter, r *http.Request, user store.AppUser) error {
	token, err := randomToken(32)
	if err != nil {
		return err
	}
	expires := time.Now().UTC().Add(time.Duration(a.cfg.Auth.SessionHours) * time.Hour)
	if err := a.store.CreateUserSession(r.Context(), tokenHash(token), user.ID, expires); err != nil {
		return err
	}
	_ = a.store.TouchUserLogin(r.Context(), user.ID)
	http.SetCookie(w, &http.Cookie{Name: authSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	return nil
}

func (a *authManager) handleMCPConfig(w http.ResponseWriter, r *http.Request) {
	user, ok := currentAuthUser(r.Context())
	if !ok {
		var err error
		user, err = a.authenticate(r)
		if err != nil {
			writeAdminError(w, http.StatusUnauthorized, "authentication_required", nil)
			return
		}
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "mcp_token_failed", err)
		return
	}
	if err := a.store.SetMCPToken(r.Context(), user.ID, tokenHash(token)); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "mcp_token_failed", err)
		return
	}
	endpoint := requestBaseURL(r) + "/mcp"
	writeJSON(w, map[string]any{"server_name": "ocr-quality", "url": endpoint, "token": token, "authorization": "Bearer " + token, "config": map[string]any{"mcpServers": map[string]any{"ocr-quality": map[string]any{"url": endpoint, "headers": map[string]string{"Authorization": "Bearer " + token}}}}}, nil)
}

func (a *authManager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if cookie, err := r.Cookie(authSessionCookie); err == nil {
		_ = a.store.DeleteUserSession(r.Context(), tokenHash(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: authSessionCookie, Path: "/", HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0)})
	writeJSON(w, map[string]string{"status": "logged_out"}, nil)
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + strings.TrimSpace(strings.Split(host, ",")[0])
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func (a *authManager) oidcProvider(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, *oauth2.Config, error) {
	cfg := a.cfg.Auth.OIDC
	if !a.cfg.Auth.Enabled || !cfg.Enabled || cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, nil, nil, errors.New("OIDC is not configured")
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, nil, nil, err
	}
	redirectURL := strings.TrimSpace(cfg.RedirectURL)
	if redirectURL == "" {
		redirectURL = strings.TrimRight(a.cfg.Review.ViewerURL, "/") + "/api/v1/auth/oidc/callback"
	}
	oauthConfig := &oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: redirectURL, Scopes: cfg.Scopes}
	return provider, provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}), oauthConfig, nil
}

func (a *authManager) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	_, _, oauthConfig, err := a.oidcProvider(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "oidc_unavailable", err)
		return
	}
	state, err := randomToken(32)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "oidc_state_failed", err)
		return
	}
	nonce, _ := randomToken(24)
	verifier := oauth2.GenerateVerifier()
	returnTo := r.URL.Query().Get("return_to")
	if !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		returnTo = "/"
	}
	if err := a.store.SaveOIDCState(r.Context(), tokenHash(state), store.OIDCLoginState{Nonce: nonce, CodeVerifier: verifier, ReturnTo: returnTo, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "oidc_state_failed", err)
		return
	}
	http.Redirect(w, r, oauthConfig.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *authManager) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	stateValue, err := a.store.ConsumeOIDCState(r.Context(), tokenHash(r.URL.Query().Get("state")), time.Now().UTC())
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "oidc_state_invalid", err)
		return
	}
	provider, verifier, oauthConfig, err := a.oidcProvider(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "oidc_unavailable", err)
		return
	}
	token, err := oauthConfig.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(stateValue.CodeVerifier))
	if err != nil {
		writeAdminError(w, http.StatusUnauthorized, "oidc_exchange_failed", err)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		writeAdminError(w, http.StatusUnauthorized, "oidc_id_token_missing", nil)
		return
	}
	idToken, err := verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		writeAdminError(w, http.StatusUnauthorized, "oidc_id_token_invalid", err)
		return
	}
	claims, err := resolveOIDCClaims(r.Context(), provider, token, idToken, stateValue.Nonce)
	if err != nil {
		writeAdminError(w, http.StatusUnauthorized, "oidc_claims_invalid", err)
		return
	}
	user, err := a.store.GetUserByOIDC(r.Context(), a.cfg.Auth.OIDC.IssuerURL, claims.Subject)
	if errors.Is(err, sql.ErrNoRows) {
		if !a.cfg.Auth.OIDC.AutoRegister {
			writeAdminError(w, http.StatusForbidden, "oidc_registration_disabled", nil)
			return
		}
		username := oidcUsername(claims)
		if existing, lookupErr := a.store.GetUserByIdentifier(r.Context(), username); lookupErr == nil && !strings.EqualFold(existing.Email, claims.Email) {
			suffix := sha256.Sum256([]byte(claims.Subject))
			username += "-" + hex.EncodeToString(suffix[:3])
		}
		user, err = a.store.UpsertOIDCUser(r.Context(), a.cfg.Auth.OIDC.IssuerURL, claims.Subject, username, claims.Email)
	}
	if err != nil || !user.Enabled {
		writeAdminError(w, http.StatusForbidden, "oidc_account_unavailable", err)
		return
	}
	if err := a.startSession(w, r, user); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "session_create_failed", err)
		return
	}
	http.Redirect(w, r, stateValue.ReturnTo, http.StatusFound)
}

func resolveOIDCClaims(ctx context.Context, provider *oidc.Provider, token *oauth2.Token, idToken *oidc.IDToken, expectedNonce string) (oidcClaims, error) {
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return oidcClaims{}, fmt.Errorf("decode OIDC ID token claims: %w", err)
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return oidcClaims{}, errors.New("OIDC subject claim is missing")
	}
	if claims.Nonce != expectedNonce {
		return oidcClaims{}, errors.New("OIDC nonce claim does not match the login request")
	}
	if strings.TrimSpace(claims.Email) == "" {
		userInfo, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
		if err != nil {
			return oidcClaims{}, fmt.Errorf("read OIDC UserInfo claims: %w", err)
		}
		var userInfoClaims oidcClaims
		if err := userInfo.Claims(&userInfoClaims); err != nil {
			return oidcClaims{}, fmt.Errorf("decode OIDC UserInfo claims: %w", err)
		}
		if userInfoClaims.Subject != claims.Subject {
			return oidcClaims{}, errors.New("OIDC UserInfo subject does not match the ID token")
		}
		claims.Email = userInfoClaims.Email
		claims.EmailVerified = userInfoClaims.EmailVerified
		if claims.PreferredUsername == "" {
			claims.PreferredUsername = userInfoClaims.PreferredUsername
		}
		if claims.Name == "" {
			claims.Name = userInfoClaims.Name
		}
	}
	claims.Email = strings.TrimSpace(claims.Email)
	if claims.Email == "" {
		return oidcClaims{}, errors.New("OIDC email claim is missing from both ID token and UserInfo")
	}
	// if claims.EmailVerified != nil && !*claims.EmailVerified {
	// 	return oidcClaims{}, errors.New("OIDC email claim is not verified")
	// }
	return claims, nil
}

func oidcUsername(claims oidcClaims) string {
	username := strings.TrimSpace(claims.PreferredUsername)
	if username == "" {
		username = strings.TrimSpace(claims.Name)
	}
	if username == "" {
		username = strings.Split(claims.Email, "@")[0]
	}
	username = strings.Map(func(value rune) rune {
		if value == ' ' || value == '/' || value == '\\' {
			return '-'
		}
		return value
	}, username)
	return username
}

func (a *authManager) authenticate(r *http.Request) (store.AppUser, error) {
	cookie, err := r.Cookie(authSessionCookie)
	if err != nil || cookie.Value == "" {
		return store.AppUser{}, sql.ErrNoRows
	}
	return a.store.UserBySession(r.Context(), tokenHash(cookie.Value), time.Now().UTC())
}

func publicUser(user store.AppUser) map[string]any {
	return map[string]any{"id": user.ID, "username": user.Username, "name": user.Username, "email": user.Email, "role": user.Role, "roles": []string{user.Role}, "enabled": user.Enabled, "auth_source": user.AuthSource, "created_at": user.CreatedAt, "updated_at": user.UpdatedAt, "last_login_at": user.LastLoginAt, "permissions": userPermissions(user.Role)}
}

func userPermissions(role string) []string {
	base := []string{"review.read", "quality.read", "quality.manage", "review.retry", "review.cancel", "review.priority"}
	if role == store.UserRoleSuperadmin {
		return append(base, "analytics.read", "usage.read", "system.read", "user.manage", "config.manage", "audit.read", "system.reconcile")
	}
	return base
}

func currentAuthUser(ctx context.Context) (store.AppUser, bool) {
	user, ok := ctx.Value(authUserKey).(store.AppUser)
	return user, ok
}

func (a *authManager) handleUsers(w http.ResponseWriter, r *http.Request) {
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		users, err := a.store.ListUsers(r.Context())
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "user_list_failed", err)
			return
		}
		result := make([]map[string]any, 0, len(users))
		for _, user := range users {
			result = append(result, publicUser(user))
		}
		writeJSON(w, result, nil)
	case http.MethodPost:
		var request userMutationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_user_body", err)
			return
		}
		hash, err := hashPassword(request.Password)
		if err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_password", err)
			return
		}
		enabled := request.Enabled == nil || *request.Enabled
		user, err := a.store.CreateUser(r.Context(), store.CreateUserInput{Username: request.Username, Email: request.Email, PasswordHash: hash, Role: request.Role, Enabled: enabled, AuthSource: "local"})
		if err != nil {
			writeAdminError(w, http.StatusConflict, "user_create_failed", err)
			return
		}
		_ = a.store.RecordAudit(r.Context(), adminActor(r.Context()), "user.create", nil, fmt.Sprintf("user_id=%d role=%s", user.ID, user.Role))
		writeJSON(w, publicUser(user), nil)
	default:
		methodNotAllowed(w)
	}
}

type configUpdateRequest struct {
	Section string          `json:"section"`
	Config  json.RawMessage `json:"config"`
}

func mergeConfigSection(target *config.Config, section string, data json.RawMessage) error {
	switch section {
	case "storage":
		var value struct {
			DatabasePath string `json:"database_path"`
			DataDir      string `json:"data_dir"`
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		target.DatabasePath, target.DataDir = value.DatabasePath, value.DataDir
		return nil
	case "gitlab":
		return json.Unmarshal(data, &target.GitLab)
	case "review":
		return json.Unmarshal(data, &target.Review)
	case "llm":
		return json.Unmarshal(data, &target.LLM)
	case "code_graph":
		return json.Unmarshal(data, &target.CodeGraph)
	case "auth":
		return json.Unmarshal(data, &target.Auth)
	case "server":
		return json.Unmarshal(data, &target.Server)
	default:
		return fmt.Errorf("unknown config section %q", section)
	}
}

func (a *authManager) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		value := a.cfg
		gitLabTokenConfigured, llmTokenConfigured := value.GitLab.Token != "", value.LLM.Token != ""
		oidcSecretConfigured, bootstrapPasswordConfigured := value.Auth.OIDC.ClientSecret != "", value.Auth.BootstrapAdmin.Password != ""
		value.GitLab.Token, value.LLM.Token, value.Auth.OIDC.ClientSecret, value.Auth.BootstrapAdmin.Password = "", "", "", ""
		writeJSON(w, map[string]any{"config": value, "path": a.cfg.SourcePath, "can_save": a.cfg.SourcePath != "", "restart_required": false, "secrets": map[string]bool{"gitlab_token": gitLabTokenConfigured, "llm_token": llmTokenConfigured, "oidc_client_secret": oidcSecretConfigured, "bootstrap_admin_password": bootstrapPasswordConfigured}}, nil)
	case http.MethodPut:
		var request configUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_config_body", err)
			return
		}
		if a.cfg.SourcePath == "" {
			writeAdminError(w, http.StatusConflict, "config_save_failed", errors.New("configuration was not loaded from a file"))
			return
		}
		persisted, err := config.ReadPersisted(a.cfg.SourcePath)
		if err != nil {
			writeAdminError(w, http.StatusConflict, "config_save_failed", err)
			return
		}
		requestConfig := persisted
		if err := mergeConfigSection(&requestConfig, request.Section, request.Config); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_config_body", err)
			return
		}
		requestConfig.SourcePath = a.cfg.SourcePath
		if requestConfig.GitLab.Token == "" {
			requestConfig.GitLab.Token = persisted.GitLab.Token
		}
		if requestConfig.LLM.Token == "" {
			requestConfig.LLM.Token = persisted.LLM.Token
		}
		if requestConfig.Auth.OIDC.ClientSecret == "" {
			requestConfig.Auth.OIDC.ClientSecret = persisted.Auth.OIDC.ClientSecret
		}
		if requestConfig.Auth.BootstrapAdmin.Password == "" {
			requestConfig.Auth.BootstrapAdmin.Password = persisted.Auth.BootstrapAdmin.Password
		}
		effective := requestConfig
		if effective.GitLab.Token == "" {
			effective.GitLab.Token = a.cfg.GitLab.Token
		}
		if effective.LLM.Token == "" {
			effective.LLM.Token = a.cfg.LLM.Token
		}
		if effective.Auth.OIDC.ClientSecret == "" {
			effective.Auth.OIDC.ClientSecret = a.cfg.Auth.OIDC.ClientSecret
		}
		if err := config.Validate(effective); err != nil {
			writeAdminError(w, http.StatusConflict, "config_invalid", err)
			return
		}
		if err := config.Save(requestConfig); err != nil {
			writeAdminError(w, http.StatusConflict, "config_save_failed", err)
			return
		}
		_ = a.store.RecordAudit(r.Context(), adminActor(r.Context()), "system.config.update", nil, "configuration section saved; restart required")
		writeJSON(w, map[string]any{"status": "saved", "restart_required": true}, nil)
	default:
		methodNotAllowed(w)
	}
}

func (a *authManager) handleUser(w http.ResponseWriter, r *http.Request) {
	if adminRole(r.Context()) != "admin" {
		writeAdminError(w, http.StatusForbidden, "superadmin_required", nil)
		return
	}
	id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"), "/"), 10, 64)
	if err != nil || id <= 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_user_id", nil)
		return
	}
	current, _ := currentAuthUser(r.Context())
	switch r.Method {
	case http.MethodPut:
		var request userMutationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_user_body", err)
			return
		}
		enabled := request.Enabled == nil || *request.Enabled
		if current.ID == id && (!enabled || request.Role != store.UserRoleSuperadmin) {
			writeAdminError(w, http.StatusConflict, "cannot_remove_own_superadmin_access", nil)
			return
		}
		var passwordHash *string
		if request.Password != "" {
			hash, hashErr := hashPassword(request.Password)
			if hashErr != nil {
				writeAdminError(w, http.StatusBadRequest, "invalid_password", hashErr)
				return
			}
			passwordHash = &hash
		}
		user, updateErr := a.store.UpdateUser(r.Context(), id, store.UpdateUserInput{Username: request.Username, Email: request.Email, PasswordHash: passwordHash, Role: request.Role, Enabled: enabled})
		if updateErr != nil {
			writeAdminError(w, http.StatusConflict, "user_update_failed", updateErr)
			return
		}
		_ = a.store.RecordAudit(r.Context(), adminActor(r.Context()), "user.update", nil, fmt.Sprintf("user_id=%d role=%s", user.ID, user.Role))
		writeJSON(w, publicUser(user), nil)
	case http.MethodDelete:
		if current.ID == id {
			writeAdminError(w, http.StatusConflict, "cannot_delete_current_user", nil)
			return
		}
		if err := a.store.DeleteUser(r.Context(), id); err != nil {
			writeAdminError(w, http.StatusConflict, "user_delete_failed", err)
			return
		}

		_ = a.store.RecordAudit(r.Context(), adminActor(r.Context()), "user.delete", nil, fmt.Sprintf("user_id=%d", id))
		writeJSON(w, map[string]string{"status": "deleted"}, nil)
	default:
		methodNotAllowed(w)
	}
}

func (a *authManager) authenticateMCP(r *http.Request) (store.AppUser, error) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return store.AppUser{}, sql.ErrNoRows
	}
	return a.store.UserByMCPToken(r.Context(), tokenHash(strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))))
}

func (a *authManager) canAccessProject(ctx context.Context, user store.AppUser, projectID int64) bool {
	if !a.cfg.Auth.Enabled || user.Role == store.UserRoleSuperadmin {
		return true
	}
	if projectID <= 0 || a.gitlab == nil {
		return false
	}
	key := fmt.Sprintf("%d:%d", user.ID, projectID)
	nowValue := time.Now()
	a.permissionMu.Lock()
	if cached, ok := a.permissions[key]; ok && cached.ExpiresAt.After(nowValue) {
		a.permissionMu.Unlock()
		return cached.Allowed
	}
	a.permissionMu.Unlock()
	gitLabUserID := a.gitLabUserID(ctx, user)
	allowed := false
	if gitLabUserID > 0 {
		_, err := a.gitlab.GetProjectMember(ctx, projectID, gitLabUserID)
		allowed = err == nil
	}
	a.permissionMu.Lock()
	a.permissions[key] = permissionCacheEntry{Allowed: allowed, ExpiresAt: nowValue.Add(5 * time.Minute)}
	a.permissionMu.Unlock()
	return allowed
}

func (a *authManager) gitLabUserID(ctx context.Context, user store.AppUser) int64 {
	nowValue := time.Now()
	a.permissionMu.Lock()
	if cached, ok := a.identities[user.ID]; ok && cached.ExpiresAt.After(nowValue) {
		a.permissionMu.Unlock()
		return cached.UserID
	}
	a.permissionMu.Unlock()
	users, err := a.gitlab.SearchUsers(ctx, user.Email)
	var id int64
	if err == nil {
		for _, candidate := range users {
			if strings.EqualFold(strings.TrimSpace(candidate.Email), user.Email) || strings.EqualFold(strings.TrimSpace(candidate.PublicEmail), user.Email) {
				id = candidate.ID
				break
			}
		}
	}
	a.permissionMu.Lock()
	a.identities[user.ID] = gitLabIdentityCacheEntry{UserID: id, ExpiresAt: nowValue.Add(10 * time.Minute)}
	a.permissionMu.Unlock()
	return id
}
func requireProjectAccess(w http.ResponseWriter, r *http.Request, auth *authManager, projectID int64) bool {
	if auth == nil || auth.requestCanAccessProject(r, projectID) {
		return true
	}
	writeAdminError(w, http.StatusForbidden, "gitlab_project_forbidden", nil)
	return false
}

func (a *authManager) requestCanAccessProject(r *http.Request, projectID int64) bool {
	if !a.cfg.Auth.Enabled {
		return true
	}
	user, ok := currentAuthUser(r.Context())
	return ok && a.canAccessProject(r.Context(), user, projectID)
}
