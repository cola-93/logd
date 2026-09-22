package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"logd/internal/monitor"
	"logd/internal/partition"
	"logd/internal/queue"
	"logd/internal/store"
	"logd/internal/web"
)

const (
	sessionCookieName = "logd_admin"
	sessionTTL        = 12 * time.Hour
)

type Options struct {
	DB                *store.DB
	Renderer          *web.Renderer
	Tokens            *store.TokenCache
	Queue             *queue.Queue
	Partitions        *partition.Manager
	Monitor           *monitor.Manager
	Location          *time.Location
	Password          string
	SessionSecret     string
	OnRetentionChange func(int)
	LastWrite         func() time.Time
	Logger            *slog.Logger
}

type Handler struct {
	db                *store.DB
	renderer          *web.Renderer
	tokens            *store.TokenCache
	queue             *queue.Queue
	partitions        *partition.Manager
	monitor           *monitor.Manager
	location          *time.Location
	passwordHash      []byte
	sessionSecret     []byte
	onRetentionChange func(int)
	lastWrite         func() time.Time
	logger            *slog.Logger
}

func New(options Options) (*Handler, error) {
	passwordHash, err := bcrypt.GenerateFromPassword(
		[]byte(options.Password),
		bcrypt.DefaultCost,
	)
	if err != nil {
		return nil, fmt.Errorf("hash admin password: %w", err)
	}
	if len(options.SessionSecret) < 32 {
		return nil, fmt.Errorf("admin.session_secret must contain at least 32 characters")
	}
	if options.OnRetentionChange == nil {
		return nil, fmt.Errorf("retention change callback is required")
	}
	if options.LastWrite == nil {
		return nil, fmt.Errorf("last write callback is required")
	}

	return &Handler{
		db:                options.DB,
		renderer:          options.Renderer,
		tokens:            options.Tokens,
		queue:             options.Queue,
		partitions:        options.Partitions,
		monitor:           options.Monitor,
		location:          options.Location,
		passwordHash:      passwordHash,
		sessionSecret:     []byte(options.SessionSecret),
		onRetentionChange: options.OnRetentionChange,
		lastWrite:         options.LastWrite,
		logger:            options.Logger,
	}, nil
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/login", h.loginPage)
	mux.HandleFunc("POST /admin/login", h.loginPageSubmit)
	mux.HandleFunc("POST /admin/logout", h.logoutPage)
	mux.HandleFunc("GET /admin/logs", h.authenticatedPage(h.logsPage))
	mux.HandleFunc("POST /admin/logs/delete-selected", h.authenticatedAPI(h.deleteSelectedLogsPage))
	mux.HandleFunc("POST /admin/logs/delete-filter", h.authenticatedAPI(h.deleteFilteredLogsPage))
	mux.HandleFunc("POST /admin/logs/delete-scope", h.authenticatedAPI(h.deleteScopedLogsPage))
	mux.HandleFunc("POST /admin/logs/lock-selected", h.authenticatedAPI(h.setSelectedLogsLockedPage))
	mux.HandleFunc("GET /admin/logs/{event_id}", h.authenticatedPage(h.detailPage))
	mux.HandleFunc("GET /admin/project-tokens", h.authenticatedPage(h.tokensPage))
	mux.HandleFunc("POST /admin/project-tokens", h.authenticatedPage(h.createTokenPage))
	mux.HandleFunc("POST /admin/project-tokens/{id}", h.authenticatedPage(h.updateTokenPage))
	mux.HandleFunc("POST /admin/project-tokens/{id}/delete", h.authenticatedPage(h.deleteTokenPage))
	mux.HandleFunc("GET /admin/settings", h.authenticatedPage(h.settingsPage))
	mux.HandleFunc("POST /admin/settings", h.authenticatedPage(h.updateSettingsPage))
	mux.HandleFunc("GET /admin/status", h.authenticatedPage(h.statusPage))

	mux.HandleFunc("POST /api/v1/admin/login", h.loginAPI)
	mux.HandleFunc("POST /api/v1/admin/logout", h.authenticatedAPI(h.logoutAPI))
	mux.HandleFunc("GET /api/v1/logs", h.authenticatedAPI(h.logsAPI))
	mux.HandleFunc("GET /api/v1/logs/{event_id}", h.authenticatedAPI(h.detailAPI))
	mux.HandleFunc("GET /api/v1/admin/project-tokens", h.authenticatedAPI(h.listTokensAPI))
	mux.HandleFunc("POST /api/v1/admin/project-tokens", h.authenticatedAPI(h.createTokenAPI))
	mux.HandleFunc("PATCH /api/v1/admin/project-tokens/{id}", h.authenticatedAPI(h.updateTokenAPI))
	mux.HandleFunc("DELETE /api/v1/admin/project-tokens/{id}", h.authenticatedAPI(h.deleteTokenAPI))
	mux.HandleFunc("GET /api/v1/admin/settings", h.authenticatedAPI(h.settingsAPI))
	mux.HandleFunc("PATCH /api/v1/admin/settings", h.authenticatedAPI(h.updateSettingsAPI))
	mux.HandleFunc("GET /api/v1/admin/status", h.authenticatedAPI(h.statusAPI))

	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(web.Assets())))
}

func (h *Handler) authenticatedPage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.sessionNonce(r); !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (h *Handler) authenticatedAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nonce, ok := h.sessionNonce(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", "admin session is invalid")
			return
		}
		if r.Method != http.MethodGet && !h.validCSRF(r, nonce) {
			writeJSONError(w, http.StatusForbidden, "INVALID_CSRF", "CSRF token is invalid")
			return
		}
		next(w, r)
	}
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.sessionNonce(r); ok {
		http.Redirect(w, r, "/admin/logs", http.StatusSeeOther)
		return
	}
	h.render(w, "login", http.StatusOK, loginPageData{
		pageData: pageData{
			Title:   "登录",
			Version: "1.0.0",
		},
	})
}

func (h *Handler) loginPageSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render(w, "login", http.StatusBadRequest, loginPageData{
			pageData: pageData{Title: "登录", Error: "请求格式错误", Version: "1.0.0"},
		})
		return
	}
	if !h.passwordMatches(r.FormValue("password")) {
		h.render(w, "login", http.StatusUnauthorized, loginPageData{
			pageData: pageData{Title: "登录", Error: "访问密码错误", Version: "1.0.0"},
		})
		return
	}

	_, err := h.issueSession(w, r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/logs", http.StatusSeeOther)
}

func (h *Handler) loginAPI(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if !h.passwordMatches(request.Password) {
		writeJSONError(w, http.StatusUnauthorized, "INVALID_PASSWORD", "访问密码错误")
		return
	}

	nonce, err := h.issueSession(w, r)
	if err != nil {
		h.logger.Error("issue admin session", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "创建会话失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"csrf_token": h.csrfToken(nonce),
	})
}

func (h *Handler) logoutPage(w http.ResponseWriter, r *http.Request) {
	nonce, ok := h.sessionNonce(r)
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	if !h.validCSRF(r, nonce) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	h.clearSession(w, r)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) logoutAPI(w http.ResponseWriter, r *http.Request) {
	h.clearSession(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) passwordMatches(password string) bool {
	return bcrypt.CompareHashAndPassword(h.passwordHash, []byte(password)) == nil
}

func (h *Handler) issueSession(w http.ResponseWriter, r *http.Request) (string, error) {
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("generate session nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	expires := time.Now().Add(sessionTTL).Unix()
	value := fmt.Sprintf(
		"%d:%s:%s",
		expires,
		nonce,
		h.sessionSignature(expires, nonce),
	)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	return nonce, nil
}

func (h *Handler) sessionNonce(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	parts := strings.Split(cookie.Value, ":")
	if len(parts) != 3 {
		return "", false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return "", false
	}
	expected := h.sessionSignature(expires, parts[1])
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return "", false
	}
	return parts[1], true
}

func (h *Handler) sessionSignature(expires int64, nonce string) string {
	return h.signature(fmt.Sprintf("session:%d:%s", expires, nonce))
}

func (h *Handler) csrfToken(nonce string) string {
	return h.signature("csrf:" + nonce)
}

func (h *Handler) validCSRF(r *http.Request, nonce string) bool {
	candidate := r.Header.Get("X-CSRF-Token")
	if candidate == "" {
		_ = r.ParseForm()
		candidate = r.FormValue("_csrf")
	}
	expected := h.csrfToken(nonce)
	return hmac.Equal([]byte(expected), []byte(candidate))
}

func (h *Handler) signature(value string) string {
	mac := hmac.New(sha256.New, h.sessionSecret)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h *Handler) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) render(w http.ResponseWriter, page string, status int, value any) {
	if err := h.renderer.Render(w, page, status, value); err != nil {
		h.logger.Error("render admin page", "page", page, "error", err)
	}
}

func (h *Handler) serverError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.Error("admin request failed", "path", r.URL.Path, "error", err)
	data := h.basePageData(r, "系统错误", "logs")
	data.Error = "系统内部错误"
	h.render(w, "logs", http.StatusInternalServerError, logsPageData{
		pageData: data,
		Levels:   logLevels,
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("请求体不是有效的 JSON")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func newUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate UUID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%s-%s-%s-%s-%s",
		hex.EncodeToString(value[0:4]),
		hex.EncodeToString(value[4:6]),
		hex.EncodeToString(value[6:8]),
		hex.EncodeToString(value[8:10]),
		hex.EncodeToString(value[10:16]),
	), nil
}
