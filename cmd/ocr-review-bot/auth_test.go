package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/config"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/gitlab"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/store"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func authTestConfig() config.Config {
	cfg := config.Default()
	cfg.Auth.Enabled = true
	cfg.Auth.SessionHours = 2
	cfg.Auth.BootstrapAdmin = config.BootstrapAdminConfig{Username: "root", Email: "root@example.com", Password: "strong-password"}
	cfg.GitLab.Token = "token"
	cfg.LLM.URL = "https://llm.example.com"
	cfg.LLM.Model = "model"
	return cfg
}

func requestJSON(t *testing.T, handler http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func loginCookie(t *testing.T, handler http.Handler, identifier, password string) *http.Cookie {
	t.Helper()
	response := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"identifier":"`+identifier+`","password":"`+password+`"}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == authSessionCookie {
			return cookie
		}
	}
	t.Fatal("session cookie missing")
	return nil
}

func TestFirstRunRequiresSetupAndCreatesSuperadminSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "setup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := authTestConfig()
	cfg.Auth.BootstrapAdmin = config.BootstrapAdminConfig{}
	handler := routes(st, gitlab.New("http://127.0.0.1:1", "token", time.Second), cfg, "")
	response := requestJSON(t, handler, http.MethodGet, "/api/v1/auth/config", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"setup_required":true`) {
		t.Fatalf("auth config = %d %s", response.Code, response.Body.String())
	}
	if response = requestJSON(t, handler, http.MethodGet, "/api/v1/admin/me", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin status = %d", response.Code)
	}
	response = requestJSON(t, handler, http.MethodPost, "/api/v1/auth/setup", `{"username":"root","email":"root@example.com","password":"strong-password"}`, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"role":"superadmin"`) {
		t.Fatalf("setup response = %d %s", response.Code, response.Body.String())
	}
	var cookie *http.Cookie
	for _, value := range response.Result().Cookies() {
		if value.Name == authSessionCookie {
			cookie = value
		}
	}
	if cookie == nil {
		t.Fatal("setup session cookie missing")
	}
	if response = requestJSON(t, handler, http.MethodGet, "/api/v1/admin/me", "", cookie); response.Code != http.StatusOK {
		t.Fatalf("setup session me = %d %s", response.Code, response.Body.String())
	}
	if response = requestJSON(t, handler, http.MethodPost, "/api/v1/auth/setup", `{"username":"other","email":"other@example.com","password":"strong-password"}`, nil); response.Code != http.StatusConflict {
		t.Fatalf("repeated setup status = %d", response.Code)
	}
}

func TestLocalAccountsRolesAndUserManagement(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := authTestConfig()
	handler := routes(st, gitlab.New("http://127.0.0.1:1", "token", time.Second), cfg, "")
	if response := requestJSON(t, handler, http.MethodGet, "/api/v1/admin/me", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", response.Code)
	}
	adminCookie := loginCookie(t, handler, "root@example.com", "strong-password")
	response := requestJSON(t, handler, http.MethodPost, "/api/v1/admin/users", `{"username":"alice","email":"alice@example.com","password":"alice-password","role":"user","enabled":true}`, adminCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("create user status = %d, body = %s", response.Code, response.Body.String())
	}
	aliceCookie := loginCookie(t, handler, "alice", "alice-password")
	response = requestJSON(t, handler, http.MethodGet, "/api/v1/admin/me", "", aliceCookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"role":"user"`) || !strings.Contains(response.Body.String(), `"email":"alice@example.com"`) {
		t.Fatalf("ordinary me = %d %s", response.Code, response.Body.String())
	}
	response = requestJSON(t, handler, http.MethodPost, "/api/v1/auth/mcp-config", "", aliceCookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mcpServers"`) || !strings.Contains(response.Body.String(), `/mcp`) {
		t.Fatalf("MCP config = %d %s", response.Code, response.Body.String())
	}
	if response = requestJSON(t, handler, http.MethodPost, "/api/v1/auth/mcp-config", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous MCP config status = %d", response.Code)
	}
	if response = requestJSON(t, handler, http.MethodGet, "/api/v1/admin/users", "", aliceCookie); response.Code != http.StatusForbidden {
		t.Fatalf("ordinary users status = %d", response.Code)
	}
	if response = requestJSON(t, handler, http.MethodGet, "/api/v1/admin/config", "", aliceCookie); response.Code != http.StatusForbidden {
		t.Fatalf("ordinary config status = %d", response.Code)
	}
	response = requestJSON(t, handler, http.MethodGet, "/api/v1/admin/config", "", adminCookie)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"token":"token"`) || strings.Contains(response.Body.String(), "strong-password") {
		t.Fatalf("superadmin config response = %d %s", response.Code, response.Body.String())
	}
}

func TestOrdinaryUserProjectsAreFilteredByGitLabEmailMembership(t *testing.T) {
	gitLabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/users":
			_, _ = io.WriteString(w, `[{"id":41,"username":"alice","email":"alice@example.com"}]`)
		case "/api/v4/projects":
			_, _ = io.WriteString(w, `[{"id":1,"name":"allowed","path_with_namespace":"group/allowed"},{"id":2,"name":"denied","path_with_namespace":"group/denied"}]`)
		case "/api/v4/projects/1/languages", "/api/v4/projects/2/languages":
			_, _ = io.WriteString(w, `{"Go":100}`)
		case "/api/v4/projects/1/members/all/41":
			_, _ = io.WriteString(w, `{"id":41,"username":"alice","email":"alice@example.com"}`)
		case "/api/v4/projects/2/members/all/41":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gitLabServer.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "permissions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := authTestConfig()
	handler := routes(st, gitlab.New(gitLabServer.URL, "token", time.Second), cfg, "")
	adminCookie := loginCookie(t, handler, "root", "strong-password")
	response := requestJSON(t, handler, http.MethodPost, "/api/v1/admin/users", `{"username":"alice","email":"alice@example.com","password":"alice-password","role":"user","enabled":true}`, adminCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("create user = %d %s", response.Code, response.Body.String())
	}
	aliceCookie := loginCookie(t, handler, "alice", "alice-password")
	response = requestJSON(t, handler, http.MethodGet, "/api/v1/admin/quality/projects", "", aliceCookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"allowed"`) || strings.Contains(response.Body.String(), `"denied"`) {
		t.Fatalf("filtered projects = %d %s", response.Code, response.Body.String())
	}
	if response = requestJSON(t, handler, http.MethodGet, "/api/v1/admin/quality/projects/2/mrs", "", aliceCookie); response.Code != http.StatusForbidden {
		t.Fatalf("denied project status = %d", response.Code)
	}
}

func TestOIDCOneClickRegistration(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key := jose.JSONWebKey{Key: &privateKey.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: privateKey, KeyID: key.KeyID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var providerServer *httptest.Server
	var nonce, codeVerifier string
	userInfoSubject := "subject-1"
	providerServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": providerServer.URL, "authorization_endpoint": providerServer.URL + "/authorize", "token_endpoint": providerServer.URL + "/token", "userinfo_endpoint": providerServer.URL + "/userinfo", "jwks_uri": providerServer.URL + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []jose.JSONWebKey{key}})
		case "/token":
			_ = r.ParseForm()
			codeVerifier = r.Form.Get("code_verifier")
			nowValue := time.Now()
			claims := jwt.Claims{Issuer: providerServer.URL, Subject: "subject-1", Audience: jwt.Audience{"client"}, Expiry: jwt.NewNumericDate(nowValue.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(nowValue)}
			raw, signErr := jwt.Signed(signer).Claims(claims).Claims(map[string]any{"preferred_username": "oidc-user", "nonce": nonce}).Serialize()
			if signErr != nil {
				t.Fatal(signErr)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "token_type": "Bearer", "id_token": raw})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("userinfo authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": userInfoSubject, "email": "oidc@example.com", "preferred_username": "oidc-user"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer providerServer.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "oidc-auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := authTestConfig()
	cfg.Auth.OIDC = config.OIDCConfig{Enabled: true, IssuerURL: providerServer.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "http://app.example.com/api/v1/auth/oidc/callback", Scopes: []string{"openid", "profile", "email"}, AutoRegister: true}
	handler := routes(st, gitlab.New("http://127.0.0.1:1", "token", time.Second), cfg, "")
	response := requestJSON(t, handler, http.MethodGet, "/api/v1/auth/oidc/login?return_to=%2Fquality", "", nil)
	if response.Code != http.StatusFound {
		t.Fatalf("OIDC login status = %d, body = %s", response.Code, response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	nonce = location.Query().Get("nonce")
	state := location.Query().Get("state")
	if state == "" || nonce == "" || location.Query().Get("code_challenge") == "" {
		t.Fatalf("OIDC authorization URL = %s", location.String())
	}
	response = requestJSON(t, handler, http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state="+url.QueryEscape(state), "", nil)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/quality" || codeVerifier == "" {
		t.Fatalf("OIDC callback = %d location=%q verifier=%q body=%s", response.Code, response.Header().Get("Location"), codeVerifier, response.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == authSessionCookie {
			sessionCookie = cookie
		}
	}
	me := requestJSON(t, handler, http.MethodGet, "/api/v1/admin/me", "", sessionCookie)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"username":"oidc-user"`) || !strings.Contains(me.Body.String(), `"email":"oidc@example.com"`) {
		t.Fatalf("OIDC me = %d %s", me.Code, me.Body.String())
	}
	if count, err := st.CountUsers(context.Background()); err != nil || count != 2 {
		t.Fatalf("user count = %d, err = %v", count, err)
	}
	userInfoSubject = "different-subject"
	response = requestJSON(t, handler, http.MethodGet, "/api/v1/auth/oidc/login", "", nil)
	location, err = url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	nonce = location.Query().Get("nonce")
	state = location.Query().Get("state")
	response = requestJSON(t, handler, http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state="+url.QueryEscape(state), "", nil)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"oidc_claims_invalid"`) {
		t.Fatalf("OIDC mismatched UserInfo subject = %d %s", response.Code, response.Body.String())
	}
}
