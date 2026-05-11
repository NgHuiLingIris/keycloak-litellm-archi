package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// SessionHandler manages HTTP requests for session operations
type SessionHandler struct {
	configStore   configstore.ConfigStore
	wsTicketStore *WSTicketStore
}

type ssoConfig struct {
	Enabled               bool
	ClientID              string
	ClientSecret          string
	AuthorizationEndpoint string
	TokenEndpoint         string
	UserinfoEndpoint      string
	RedirectURI           string
	Scopes                string
	LoginRedirect         string
}

type sessionProfile struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Provider  string `json:"provider,omitempty"`
}

// NewSessionHandler creates a new session handler instance
func NewSessionHandler(configStore configstore.ConfigStore, wsTicketStore *WSTicketStore) *SessionHandler {
	return &SessionHandler{
		configStore:   configStore,
		wsTicketStore: wsTicketStore,
	}
}

// RegisterRoutes registers the session-related routes
func (h *SessionHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/api/session/login", lib.ChainMiddlewares(h.login, middlewares...))
	r.POST("/api/session/logout", lib.ChainMiddlewares(h.logout, middlewares...))
	r.GET("/api/session/is-auth-enabled", lib.ChainMiddlewares(h.isAuthEnabled, middlewares...))
	r.GET("/api/session/me", lib.ChainMiddlewares(h.me, middlewares...))
	r.GET("/api/session/directory-users", lib.ChainMiddlewares(h.directoryUsers, middlewares...))
	r.GET("/api/session/sso/login", lib.ChainMiddlewares(h.ssoLogin, middlewares...))
	r.GET("/api/session/sso/callback", lib.ChainMiddlewares(h.ssoCallback, middlewares...))
	r.POST("/api/session/ws-ticket", lib.ChainMiddlewares(h.issueWSTicket, middlewares...))
}

// isAuthEnabled handles GET /api/session/is-auth-enabled - Check if auth is enabled
func (h *SessionHandler) isAuthEnabled(ctx *fasthttp.RequestCtx) {
	sso := getSSOConfig()
	if h.configStore == nil {
		SendJSON(ctx, map[string]any{
			"is_auth_enabled": false,
			"sso_enabled":     sso.Enabled,
			"sso_login_url":   ssoLoginURL(sso),
		})
		return
	}
	authConfig, err := h.configStore.GetAuthConfig(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get auth config: %v", err))
		return
	}
	if authConfig == nil {
		SendJSON(ctx, map[string]any{
			"is_auth_enabled": false,
			"sso_enabled":     sso.Enabled,
			"sso_login_url":   ssoLoginURL(sso),
		})
		return
	}
	// Check if the header has a token and is valid (Authorization header or cookie)
	token := ""
	if authHeader := string(ctx.Request.Header.Peek("Authorization")); strings.HasPrefix(authHeader, "Bearer ") {
		token = strings.TrimPrefix(authHeader, "Bearer ")
	}
	if token == "" {
		token = string(ctx.Request.Header.Cookie("token"))
	}
	hasValidToken := false
	if token != "" {
		session, err := h.configStore.GetSession(ctx, token)
		if err == nil && session != nil && session.ExpiresAt.After(time.Now()) {
			hasValidToken = true
		}
	}
	SendJSON(ctx, map[string]any{
		"is_auth_enabled": authConfig.IsEnabled,
		"has_valid_token": hasValidToken,
		"sso_enabled":     sso.Enabled,
		"sso_login_url":   ssoLoginURL(sso),
	})
}

// login handles POST /api/session/login - Login a user
func (h *SessionHandler) login(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusForbidden, "Authentication is not enabled")
		return
	}
	payload := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{}
	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Get auth config
	authConfig, err := h.configStore.GetAuthConfig(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get auth config: %v", err))
		return
	}

	// Check if auth is enabled
	if authConfig == nil || !authConfig.IsEnabled {
		SendError(ctx, fasthttp.StatusForbidden, "Authentication is not enabled")
		return
	}

	// Verify credentials
	if payload.Username != authConfig.AdminUserName.GetValue() {
		SendError(ctx, fasthttp.StatusUnauthorized, "Invalid username or password")
		return
	}
	compare, err := encrypt.CompareHash(authConfig.AdminPassword.GetValue(), payload.Password)
	if err != nil {
		SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
		return
	}
	if !compare {
		SendError(ctx, fasthttp.StatusUnauthorized, "Invalid username or password")
		return
	}

	// Creating a new session
	if _, err := h.createSessionCookie(ctx); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create session: %v", err))
		return
	}
	setProfileCookie(ctx, sessionProfile{
		ID:       "local-admin",
		Name:     authConfig.AdminUserName.GetValue(),
		Provider: "local",
	})

	SendJSON(ctx, map[string]any{
		"message": "Login successful",
	})
}

func (h *SessionHandler) ssoLogin(ctx *fasthttp.RequestCtx) {
	cfg := getSSOConfig()
	if !cfg.Enabled {
		SendError(ctx, fasthttp.StatusNotFound, "SSO login is not enabled")
		return
	}
	state, err := randomState()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to start SSO login")
		return
	}
	setCookie(ctx, "bifrost_sso_state", state, time.Now().Add(10*time.Minute))

	values := url.Values{}
	values.Set("client_id", cfg.ClientID)
	values.Set("redirect_uri", cfg.RedirectURI)
	values.Set("response_type", "code")
	values.Set("scope", cfg.Scopes)
	values.Set("state", state)

	authURL := cfg.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + values.Encode()
	} else {
		authURL += "?" + values.Encode()
	}
	ctx.Redirect(authURL, fasthttp.StatusFound)
}

func (h *SessionHandler) ssoCallback(ctx *fasthttp.RequestCtx) {
	cfg := getSSOConfig()
	if !cfg.Enabled {
		SendError(ctx, fasthttp.StatusNotFound, "SSO login is not enabled")
		return
	}
	if h.configStore == nil {
		redirectWithSSOError(ctx, "Authentication is not enabled")
		return
	}
	authConfig, err := h.configStore.GetAuthConfig(ctx)
	if err != nil || authConfig == nil || !authConfig.IsEnabled {
		redirectWithSSOError(ctx, "Authentication is not enabled")
		return
	}

	requestState := string(ctx.QueryArgs().Peek("state"))
	cookieState := string(ctx.Request.Header.Cookie("bifrost_sso_state"))
	clearCookie(ctx, "bifrost_sso_state")
	if requestState == "" || cookieState == "" || requestState != cookieState {
		redirectWithSSOError(ctx, "Invalid SSO state")
		return
	}

	if providerErr := string(ctx.QueryArgs().Peek("error")); providerErr != "" {
		description := string(ctx.QueryArgs().Peek("error_description"))
		if description != "" {
			providerErr = providerErr + ": " + description
		}
		redirectWithSSOError(ctx, providerErr)
		return
	}

	code := string(ctx.QueryArgs().Peek("code"))
	if code == "" {
		redirectWithSSOError(ctx, "Missing SSO authorization code")
		return
	}

	tokenResp, err := exchangeSSOCode(cfg, code)
	if err != nil {
		logger.Error("sso token exchange failed: %v", err)
		redirectWithSSOError(ctx, "Unable to complete SSO login")
		return
	}
	if tokenResp.AccessToken == "" {
		logger.Error("sso token response did not include an access token")
		redirectWithSSOError(ctx, "Unable to complete SSO login")
		return
	}
	profile := sessionProfile{Provider: "sso"}
	if cfg.UserinfoEndpoint != "" {
		userinfoProfile, err := fetchSSOUserinfo(cfg, tokenResp.AccessToken)
		if err != nil {
			logger.Warn("sso userinfo lookup failed: %v", err)
		} else {
			profile = userinfoProfile
		}
	}
	if _, err := h.createSessionCookie(ctx); err != nil {
		logger.Error("sso session creation failed: %v", err)
		redirectWithSSOError(ctx, "Unable to create dashboard session")
		return
	}
	setProfileCookie(ctx, profile)
	ctx.Redirect(cfg.LoginRedirect, fasthttp.StatusFound)
}

func (h *SessionHandler) me(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusForbidden, "Authentication is not enabled")
		return
	}

	if profile, ok := profileFromCookie(ctx); ok {
		SendJSON(ctx, map[string]any{
			"user": profile,
		})
		return
	}

	authConfig, err := h.configStore.GetAuthConfig(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get auth config: %v", err))
		return
	}
	name := "Admin"
	if authConfig != nil && authConfig.AdminUserName != nil {
		name = authConfig.AdminUserName.GetValue()
	}

	SendJSON(ctx, map[string]any{
		"user": sessionProfile{
			ID:       "local-admin",
			Name:     name,
			Provider: "local",
		},
	})
}

func (h *SessionHandler) directoryUsers(ctx *fasthttp.RequestCtx) {
	limit := parsePositiveInt(string(ctx.QueryArgs().Peek("limit")), 100)
	offset := parseNonNegativeInt(string(ctx.QueryArgs().Peek("offset")), 0)
	search := strings.TrimSpace(string(ctx.QueryArgs().Peek("search")))

	result, err := fetchKeycloakUsers(limit, offset, search)
	if err != nil {
		logger.Warn("failed to fetch Keycloak directory users: %v", err)
		SendError(ctx, fasthttp.StatusBadGateway, "Failed to fetch registered users from Keycloak")
		return
	}
	SendJSON(ctx, result)
}

// logout handles POST /api/session/logout - Logout a user
func (h *SessionHandler) logout(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusForbidden, "Authentication is not enabled")
		return
	}
	// Get token from Authorization header
	token := string(ctx.Request.Header.Peek("Authorization"))
	token = strings.TrimPrefix(token, "Bearer ")

	// If no token in header, try to get from cookie
	if token == "" {
		token = string(ctx.Request.Header.Cookie("token"))
	}

	// clear token from cookies
	cookie := fasthttp.AcquireCookie()
	defer fasthttp.ReleaseCookie(cookie)
	cookie.SetKey("token")
	cookie.SetValue("")
	cookie.SetExpire(time.Now().Add(-time.Hour * 24 * 30))
	cookie.SetPath("/")
	cookie.SetHTTPOnly(true)
	cookie.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	// Check if source is https then set secure
	if string(ctx.Request.Header.Peek("X-Forwarded-Proto")) == "https" {
		cookie.SetSecure(true)
	}
	ctx.Response.Header.SetCookie(cookie)
	clearCookie(ctx, "bifrost_session_profile")

	// delete session from database if token exists
	if token != "" {
		err := h.configStore.DeleteSession(ctx, token)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			logger.Error("failed to delete session during logout: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to invalidate session. Please try again.")
			return
		}
	}

	SendJSON(ctx, map[string]any{
		"message": "Logout successful",
	})
}

// issueWSTicket handles POST /api/session/ws-ticket - Issue a short-lived ticket for WebSocket auth.
// The caller must already be authenticated (via cookie or Authorization header).
// Returns a one-time-use ticket that the frontend passes as ?ticket= when opening the WebSocket.
func (h *SessionHandler) issueWSTicket(ctx *fasthttp.RequestCtx) {
	if h.wsTicketStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "WebSocket tickets are not available")
		return
	}
	sessionToken, ok := ctx.UserValue(schemas.BifrostContextKeySessionToken).(string)
	if !ok {
		SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
		return
	}
	if sessionToken == "" {
		// This is the case where auth is not configured or not enabled
		sessionToken = "dummy-session"
	}
	ticket, err := h.wsTicketStore.Issue(sessionToken)
	if err != nil {
		logger.Error("failed to issue WS ticket: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to issue WebSocket ticket")
		return
	}
	SendJSON(ctx, map[string]any{
		"ticket": ticket,
	})
}

func (h *SessionHandler) createSessionCookie(ctx *fasthttp.RequestCtx) (string, error) {
	token := uuid.New().String()
	expiresAt := time.Now().Add(time.Hour * 24 * 30)
	session := &tables.SessionsTable{
		Token:     token,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := h.configStore.CreateSession(ctx, session); err != nil {
		return "", err
	}
	setCookie(ctx, "token", token, expiresAt)
	return token, nil
}

func getSSOConfig() ssoConfig {
	cfg := ssoConfig{
		Enabled:               envBool("BIFROST_SSO_ENABLED"),
		ClientID:              strings.TrimSpace(os.Getenv("BIFROST_SSO_CLIENT_ID")),
		ClientSecret:          os.Getenv("BIFROST_SSO_CLIENT_SECRET"),
		AuthorizationEndpoint: strings.TrimSpace(os.Getenv("BIFROST_SSO_AUTHORIZATION_ENDPOINT")),
		TokenEndpoint:         strings.TrimSpace(os.Getenv("BIFROST_SSO_TOKEN_ENDPOINT")),
		UserinfoEndpoint:      strings.TrimSpace(os.Getenv("BIFROST_SSO_USERINFO_ENDPOINT")),
		RedirectURI:           strings.TrimSpace(os.Getenv("BIFROST_SSO_REDIRECT_URI")),
		Scopes:                strings.TrimSpace(os.Getenv("BIFROST_SSO_SCOPES")),
		LoginRedirect:         strings.TrimSpace(os.Getenv("BIFROST_SSO_LOGIN_REDIRECT")),
	}
	if cfg.Scopes == "" {
		cfg.Scopes = "openid email profile"
	}
	if cfg.LoginRedirect == "" {
		cfg.LoginRedirect = "/workspace"
	}
	if cfg.Enabled && (cfg.ClientID == "" || cfg.AuthorizationEndpoint == "" || cfg.TokenEndpoint == "" || cfg.RedirectURI == "") {
		logger.Warn("BIFROST_SSO_ENABLED is true but required SSO configuration is missing")
		cfg.Enabled = false
	}
	return cfg
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func parsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	if parsed > 500 {
		return 500
	}
	return parsed
}

func parseNonNegativeInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func ssoLoginURL(cfg ssoConfig) string {
	if !cfg.Enabled {
		return ""
	}
	return "/api/session/sso/login"
}

func randomState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func setCookie(ctx *fasthttp.RequestCtx, key, value string, expiresAt time.Time) {
	cookie := fasthttp.AcquireCookie()
	defer fasthttp.ReleaseCookie(cookie)
	cookie.SetKey(key)
	cookie.SetValue(value)
	cookie.SetExpire(expiresAt)
	cookie.SetPath("/")
	cookie.SetHTTPOnly(true)
	cookie.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	if string(ctx.Request.Header.Peek("X-Forwarded-Proto")) == "https" {
		cookie.SetSecure(true)
	}
	ctx.Response.Header.SetCookie(cookie)
}

func clearCookie(ctx *fasthttp.RequestCtx, key string) {
	setCookie(ctx, key, "", time.Now().Add(-time.Hour*24*30))
}

func setProfileCookie(ctx *fasthttp.RequestCtx, profile sessionProfile) {
	if profile.Provider == "" {
		profile.Provider = "sso"
	}
	payload, err := json.Marshal(profile)
	if err != nil {
		logger.Warn("failed to encode session profile: %v", err)
		return
	}
	setCookie(ctx, "bifrost_session_profile", base64.RawURLEncoding.EncodeToString(payload), time.Now().Add(time.Hour*24*30))
}

func profileFromCookie(ctx *fasthttp.RequestCtx) (sessionProfile, bool) {
	value := string(ctx.Request.Header.Cookie("bifrost_session_profile"))
	if value == "" {
		return sessionProfile{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return sessionProfile{}, false
	}
	profile := sessionProfile{}
	if err := json.Unmarshal(payload, &profile); err != nil {
		return sessionProfile{}, false
	}
	if profile.ID == "" && profile.Name == "" && profile.Email == "" {
		return sessionProfile{}, false
	}
	return profile, true
}

type ssoTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type keycloakUser struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email,omitempty"`
	FirstName     string `json:"firstName,omitempty"`
	LastName      string `json:"lastName,omitempty"`
	Enabled       bool   `json:"enabled"`
	Created       int64  `json:"createdTimestamp,omitempty"`
	EmailVerified bool   `json:"emailVerified,omitempty"`
}

type directoryUser struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Name          string `json:"name"`
	Email         string `json:"email,omitempty"`
	FirstName     string `json:"first_name,omitempty"`
	LastName      string `json:"last_name,omitempty"`
	Enabled       bool   `json:"enabled"`
	EmailVerified bool   `json:"email_verified"`
	CreatedAt     string `json:"created_at,omitempty"`
}

type directoryUsersResponse struct {
	Users      []directoryUser `json:"users"`
	Count      int             `json:"count"`
	TotalCount int             `json:"total_count"`
	Source     string          `json:"source"`
}

func exchangeSSOCode(cfg ssoConfig, code string) (*ssoTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", cfg.RedirectURI)
	form.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}

	req, err := http.NewRequest(http.MethodPost, cfg.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	tokenResp := &ssoTokenResponse{}
	if err := json.Unmarshal(body, tokenResp); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || tokenResp.Error != "" {
		if tokenResp.ErrorDescription != "" {
			return nil, fmt.Errorf("token endpoint returned %s: %s", tokenResp.Error, tokenResp.ErrorDescription)
		}
		return nil, fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}
	return tokenResp, nil
}

func fetchKeycloakUsers(limit, offset int, search string) (*directoryUsersResponse, error) {
	baseURL := strings.TrimRight(os.Getenv("BIFROST_KEYCLOAK_ADMIN_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(os.Getenv("BIFROST_KEYCLOAK_BASE_URL"), "/")
	}
	if baseURL == "" {
		return nil, fmt.Errorf("BIFROST_KEYCLOAK_ADMIN_BASE_URL is not configured")
	}
	realm := strings.TrimSpace(os.Getenv("BIFROST_KEYCLOAK_REALM"))
	if realm == "" {
		realm = "master"
	}
	username := strings.TrimSpace(os.Getenv("BIFROST_KEYCLOAK_ADMIN_USERNAME"))
	password := os.Getenv("BIFROST_KEYCLOAK_ADMIN_PASSWORD")
	if username == "" || password == "" {
		return nil, fmt.Errorf("BIFROST_KEYCLOAK_ADMIN_USERNAME/PASSWORD are not configured")
	}

	token, err := fetchKeycloakAdminToken(baseURL, realm, username, password)
	if err != nil {
		return nil, err
	}

	usersURL := fmt.Sprintf("%s/admin/realms/%s/users", baseURL, url.PathEscape(realm))
	values := url.Values{}
	values.Set("first", strconv.Itoa(offset))
	values.Set("max", strconv.Itoa(limit))
	values.Set("briefRepresentation", "false")
	if search != "" {
		values.Set("search", search)
	}
	usersURL += "?" + values.Encode()

	body, err := authenticatedKeycloakGet(usersURL, token)
	if err != nil {
		return nil, err
	}
	keycloakUsers := []keycloakUser{}
	if err := json.Unmarshal(body, &keycloakUsers); err != nil {
		return nil, err
	}

	totalCount := len(keycloakUsers)
	countURL := fmt.Sprintf("%s/admin/realms/%s/users/count", baseURL, url.PathEscape(realm))
	if search != "" {
		countURL += "?search=" + url.QueryEscape(search)
	}
	if countBody, err := authenticatedKeycloakGet(countURL, token); err == nil {
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(string(countBody))); parseErr == nil {
			totalCount = parsed
		}
	}

	users := make([]directoryUser, 0, len(keycloakUsers))
	for _, user := range keycloakUsers {
		name := strings.TrimSpace(strings.TrimSpace(user.FirstName + " " + user.LastName))
		if name == "" {
			name = user.Username
		}
		if name == "" {
			name = user.Email
		}
		createdAt := ""
		if user.Created > 0 {
			createdAt = time.UnixMilli(user.Created).UTC().Format(time.RFC3339)
		}
		users = append(users, directoryUser{
			ID:            user.ID,
			Username:      user.Username,
			Name:          name,
			Email:         user.Email,
			FirstName:     user.FirstName,
			LastName:      user.LastName,
			Enabled:       user.Enabled,
			EmailVerified: user.EmailVerified,
			CreatedAt:     createdAt,
		})
	}

	return &directoryUsersResponse{
		Users:      users,
		Count:      len(users),
		TotalCount: totalCount,
		Source:     "keycloak",
	}, nil
}

func fetchKeycloakAdminToken(baseURL, realm, username, password string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("client_id", "admin-cli")
	form.Set("username", username)
	form.Set("password", password)

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", baseURL, url.PathEscape(realm))
	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	tokenResp := &ssoTokenResponse{}
	if err := json.Unmarshal(body, tokenResp); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || tokenResp.AccessToken == "" {
		if tokenResp.ErrorDescription != "" {
			return "", fmt.Errorf("Keycloak admin token request failed: %s", tokenResp.ErrorDescription)
		}
		return "", fmt.Errorf("Keycloak admin token request returned status %d", resp.StatusCode)
	}
	return tokenResp.AccessToken, nil
}

func authenticatedKeycloakGet(requestURL, token string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Keycloak request returned status %d", resp.StatusCode)
	}
	return body, nil
}

func fetchSSOUserinfo(cfg ssoConfig, accessToken string) (sessionProfile, error) {
	req, err := http.NewRequest(http.MethodGet, cfg.UserinfoEndpoint, nil)
	if err != nil {
		return sessionProfile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return sessionProfile{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return sessionProfile{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return sessionProfile{}, fmt.Errorf("userinfo endpoint returned status %d", resp.StatusCode)
	}

	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		return sessionProfile{}, err
	}
	profile := sessionProfile{
		ID:        claimString(claims, "sub"),
		Name:      firstClaimString(claims, "name", "preferred_username", "email"),
		Email:     claimString(claims, "email"),
		AvatarURL: claimString(claims, "picture"),
		Provider:  "sso",
	}
	return profile, nil
}

func redirectWithSSOError(ctx *fasthttp.RequestCtx, message string) {
	ctx.Redirect("/login?sso_error="+url.QueryEscape(message), fasthttp.StatusFound)
}

func firstClaimString(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := claimString(claims, key); value != "" {
			return value
		}
	}
	return ""
}

func claimString(claims map[string]any, key string) string {
	value, ok := claims[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}
