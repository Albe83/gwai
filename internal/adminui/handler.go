package adminui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Albe83/gwai/internal/platform"
)

const (
	defaultSessionTTL       = 30 * time.Minute
	defaultRequestTimeout   = 20 * time.Second
	loginChallengeTTL       = 5 * time.Minute
	defaultMaxForm          = 128 << 10
	defaultCookieName       = "gwai_admin_session"
	loginChallengeNonceSize = 32
	loginChallengeKeySize   = 32
)

type Config struct {
	AdminToken     string
	SessionTTL     time.Duration
	RequestTimeout time.Duration
	CookieName     string
	CookieSecure   bool
	MaxFormBytes   int64
	Now            func() time.Time
	Random         io.Reader
}

type server struct {
	api          API
	adminToken   string
	cookieName   string
	cookieSecure bool
	maxFormBytes int64
	sessions     *sessionStore
	views        *renderer
	now          func() time.Time
	loginKey     []byte
}

func NewHandler(api API, config Config, logger *slog.Logger) (http.Handler, error) {
	if api == nil {
		return nil, errors.New("admin API client is required")
	}
	if config.AdminToken == "" || strings.IndexFunc(config.AdminToken, unicode.IsSpace) >= 0 {
		return nil, errors.New("admin token is required and must not contain whitespace")
	}
	if config.SessionTTL == 0 {
		config.SessionTTL = defaultSessionTTL
	}
	if config.SessionTTL < time.Minute {
		return nil, errors.New("session TTL must be at least one minute")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = defaultRequestTimeout
	}
	if config.RequestTimeout <= 0 {
		return nil, errors.New("request timeout must be positive")
	}
	if config.CookieName == "" {
		config.CookieName = defaultCookieName
	}
	if err := (&http.Cookie{Name: config.CookieName, Value: "validation"}).Valid(); err != nil {
		return nil, fmt.Errorf("invalid cookie name: %w", err)
	}
	if config.MaxFormBytes == 0 {
		config.MaxFormBytes = defaultMaxForm
	}
	if config.MaxFormBytes <= 0 {
		return nil, errors.New("maximum form size must be positive")
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	loginKey := make([]byte, loginChallengeKeySize)
	if _, err := io.ReadFull(config.Random, loginKey); err != nil {
		return nil, fmt.Errorf("initialize login challenge key: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	views, err := newRenderer()
	if err != nil {
		return nil, err
	}
	s := &server{
		api: api, adminToken: config.AdminToken,
		cookieName: config.CookieName, cookieSecure: config.CookieSecure,
		maxFormBytes: config.MaxFormBytes,
		sessions:     newSessionStore(config.Now, config.Random, config.SessionTTL),
		views:        views, now: config.Now, loginKey: loginKey,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", s.health)
	mux.HandleFunc("GET /readyz", s.health)
	mux.Handle("GET /assets/", s.assets())
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.auth(s.logout))
	mux.HandleFunc("GET /{$}", s.auth(s.dashboard))

	mux.HandleFunc("GET /users", s.auth(s.listUsers))
	mux.HandleFunc("GET /users/new", s.auth(s.newUser))
	mux.HandleFunc("POST /users", s.auth(s.createUser))
	mux.HandleFunc("GET /users/{id}/edit", s.auth(s.editUser))
	mux.HandleFunc("POST /users/{id}", s.auth(s.updateUser))
	mux.HandleFunc("GET /users/{id}/status", s.auth(s.confirmUserStatus))
	mux.HandleFunc("POST /users/{id}/status", s.auth(s.changeUserStatus))
	mux.HandleFunc("GET /users/{id}/delete", s.auth(s.confirmDeleteUser))
	mux.HandleFunc("POST /users/{id}/delete", s.auth(s.deleteUser))

	mux.HandleFunc("GET /providers", s.auth(s.listProviders))
	mux.HandleFunc("GET /providers/new", s.auth(s.newProvider))
	mux.HandleFunc("POST /providers", s.auth(s.createProvider))
	mux.HandleFunc("GET /providers/{id}/edit", s.auth(s.editProvider))
	mux.HandleFunc("POST /providers/{id}", s.auth(s.updateProvider))
	mux.HandleFunc("GET /providers/{id}/status", s.auth(s.confirmProviderStatus))
	mux.HandleFunc("POST /providers/{id}/status", s.auth(s.changeProviderStatus))
	mux.HandleFunc("GET /providers/{id}/delete", s.auth(s.confirmDeleteProvider))
	mux.HandleFunc("POST /providers/{id}/delete", s.auth(s.deleteProvider))

	mux.HandleFunc("GET /models", s.auth(s.listModels))
	mux.HandleFunc("GET /models/new", s.auth(s.newModel))
	mux.HandleFunc("POST /models", s.auth(s.createModel))
	mux.HandleFunc("GET /models/{id}/edit", s.auth(s.editModel))
	mux.HandleFunc("POST /models/{id}", s.auth(s.updateModel))
	mux.HandleFunc("GET /models/{id}/status", s.auth(s.confirmModelStatus))
	mux.HandleFunc("POST /models/{id}/status", s.auth(s.changeModelStatus))
	mux.HandleFunc("GET /models/{id}/delete", s.auth(s.confirmDeleteModel))
	mux.HandleFunc("POST /models/{id}/delete", s.auth(s.deleteModel))

	mux.HandleFunc("GET /virtual-keys", s.auth(s.listVirtualKeys))
	mux.HandleFunc("GET /virtual-keys/new", s.auth(s.newVirtualKey))
	mux.HandleFunc("POST /virtual-keys", s.auth(s.createVirtualKey))
	mux.HandleFunc("GET /virtual-keys/{id}/edit", s.auth(s.editVirtualKey))
	mux.HandleFunc("POST /virtual-keys/{id}", s.auth(s.updateVirtualKey))
	mux.HandleFunc("GET /virtual-keys/{id}/status", s.auth(s.confirmVirtualKeyStatus))
	mux.HandleFunc("POST /virtual-keys/{id}/status", s.auth(s.changeVirtualKeyStatus))
	mux.HandleFunc("GET /virtual-keys/{id}/delete", s.auth(s.confirmDeleteVirtualKey))
	mux.HandleFunc("POST /virtual-keys/{id}/delete", s.auth(s.deleteVirtualKey))

	return platform.HTTPMiddleware(logger, s.securityHeaders(requestDeadline(config.RequestTimeout, mux))), nil
}

func requestDeadline(timeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	platform.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if s.cookieSecure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) assets() http.Handler {
	assets, err := staticFiles()
	if err != nil {
		return http.NotFoundHandler()
	}
	files := http.StripPrefix("/assets/", http.FileServerFS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		files.ServeHTTP(w, r)
	})
}

func (s *server) setSessionCookie(w http.ResponseWriter, session sessionSnapshot) {
	maxAge := int(session.ExpiresAt.Sub(s.now()).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: session.ID, Path: "/",
		Expires: session.ExpiresAt, MaxAge: maxAge,
		HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) setLoginChallengeCookie(w http.ResponseWriter, challenge string, expires time.Time) {
	maxAge := int(expires.Sub(s.now()).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: challenge, Path: "/",
		Expires: expires, MaxAge: maxAge,
		HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) newLoginChallenge() (string, time.Time, error) {
	nonce, err := s.sessions.randomValue(loginChallengeNonceSize)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := s.now().Add(loginChallengeTTL)
	payload := strconv.FormatInt(expires.Unix(), 10) + "." + nonce
	mac := hmac.New(sha256.New, s.loginKey)
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, expires, nil
}

func (s *server) validLoginChallenge(challenge string) bool {
	parts := strings.Split(challenge, ".")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return false
	}
	expiresUnix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || !time.Unix(expiresUnix, 0).After(s.now()) {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(nonce) != loginChallengeNonceSize {
		return false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.loginKey)
	_, _ = mac.Write([]byte(payload))
	provided, err := base64.RawURLEncoding.DecodeString(parts[2])
	return err == nil && hmac.Equal(provided, mac.Sum(nil))
}

func (s *server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: "", Path: "/", Expires: time.Unix(1, 0),
		MaxAge: -1, HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) cookieSession(r *http.Request) (sessionSnapshot, bool) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		return sessionSnapshot{}, false
	}
	return s.sessions.load(cookie.Value)
}

type authenticatedHandler func(http.ResponseWriter, *http.Request, sessionSnapshot)

func (s *server) auth(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.cookieSession(r)
		if !ok || !session.Authenticated {
			if ok {
				s.sessions.destroy(session.ID)
			}
			s.clearSessionCookie(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r, session)
	}
}

func (s *server) basePage(session sessionSnapshot, title, section string) pageData {
	return pageData{
		Title: title, Section: section, Authenticated: session.Authenticated, CSRFToken: session.CSRFToken,
		Flashes: s.sessions.takeFlashes(session.ID),
	}
}

func (s *server) parseProtectedForm(w http.ResponseWriter, r *http.Request, session sessionSnapshot) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid form", "The submitted form could not be decoded.")
		return false
	}
	if !s.sessions.verifyCSRF(session.ID, r.PostForm.Get("_csrf")) {
		s.renderSimpleError(w, session, http.StatusForbidden, "Request rejected", "The form expired or its CSRF token is invalid. Reload the page and try again.")
		return false
	}
	return true
}

func (s *server) renderSimpleError(w http.ResponseWriter, session sessionSnapshot, status int, title, detail string) {
	data := s.basePage(session, title, "")
	data.StatusCode = status
	data.Error = &viewError{Title: title, Detail: detail}
	s.views.render(w, status, "error", data)
}

func (s *server) renderOperationError(w http.ResponseWriter, session sessionSnapshot, data pageData, templateName string, err error) {
	data.Error, data.StatusCode = errorView(err)
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.Ambiguous {
		data.Error = &viewError{
			Title:     "Operation outcome unknown",
			Detail:    "The request may have completed even though its response was not received safely. Reload the relevant resource list and inspect current state before retrying.",
			RequestID: apiError.Instance,
		}
		templateName = "error"
	}
	if data.Title == "" {
		data.Title = "Operation failed"
	}
	s.views.render(w, data.StatusCode, templateName, data)
}

func (s *server) renderInvalidForm(w http.ResponseWriter, data pageData, templateName, detail string) {
	data.Error = &viewError{Title: "Invalid form", Detail: detail}
	data.StatusCode = http.StatusBadRequest
	s.views.render(w, http.StatusBadRequest, templateName, data)
}

func (s *server) flashRedirect(w http.ResponseWriter, r *http.Request, session sessionSnapshot, location, kind, message string) {
	s.sessions.addFlash(session.ID, flashMessage{Kind: kind, Message: message})
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	if existing, ok := s.cookieSession(r); ok {
		if existing.Authenticated {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	challenge, expires, err := s.newLoginChallenge()
	if err != nil {
		s.views.render(w, http.StatusServiceUnavailable, "error", pageData{
			Title: "Sign in unavailable", StatusCode: http.StatusServiceUnavailable,
			Error: &viewError{Title: "Sign in unavailable", Detail: "A secure session could not be created."},
		})
		return
	}
	s.setLoginChallengeCookie(w, challenge, expires)
	s.views.render(w, http.StatusOK, "login", pageData{Title: "Sign in", CSRFToken: challenge})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if session, ok := s.cookieSession(r); ok && session.Authenticated {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.views.render(w, http.StatusBadRequest, "login", pageData{
			Title: "Sign in",
			Error: &viewError{Title: "Invalid form", Detail: "The submitted form could not be decoded."},
		})
		return
	}
	cookie, cookieErr := r.Cookie(s.cookieName)
	candidate := r.PostForm.Get("_csrf")
	if cookieErr != nil || candidate == "" || !platform.SecureEqual(cookie.Value, candidate) || !s.validLoginChallenge(candidate) {
		challenge, expires, err := s.newLoginChallenge()
		if err == nil {
			s.setLoginChallengeCookie(w, challenge, expires)
		}
		s.views.render(w, http.StatusForbidden, "login", pageData{
			Title: "Sign in", CSRFToken: challenge,
			Error: &viewError{Title: "Request rejected", Detail: "The sign-in form expired or its CSRF token is invalid. Reload the page and try again."},
		})
		return
	}
	if !platform.SecureEqual(r.PostForm.Get("admin_token"), s.adminToken) {
		data := pageData{
			Title: "Sign in", CSRFToken: candidate,
			Error: &viewError{Title: "Sign in failed", Detail: "The administrator token is not valid."},
		}
		s.views.render(w, http.StatusUnauthorized, "login", data)
		return
	}
	authenticated, err := s.sessions.create(true)
	if err != nil {
		s.clearSessionCookie(w)
		s.views.render(w, http.StatusServiceUnavailable, "error", pageData{
			Title: "Sign in unavailable", StatusCode: http.StatusServiceUnavailable,
			Error: &viewError{Title: "Sign in unavailable", Detail: "A secure session could not be created."},
		})
		return
	}
	s.setSessionCookie(w, authenticated)
	s.sessions.addFlash(authenticated.ID, flashMessage{Kind: "success", Message: "Signed in successfully."})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	s.sessions.destroy(session.ID)
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	data := s.basePage(session, "Dashboard", "dashboard")
	var usersErr, providersErr, modelsErr, keysErr error
	var wait sync.WaitGroup
	wait.Add(4)
	go func() {
		defer wait.Done()
		data.Users, usersErr = s.api.ListUsers(r.Context())
	}()
	go func() {
		defer wait.Done()
		data.Providers, providersErr = s.api.ListProviders(r.Context())
	}()
	go func() {
		defer wait.Done()
		data.Models, modelsErr = s.api.ListModels(r.Context())
	}()
	go func() {
		defer wait.Done()
		data.VirtualKeys, keysErr = s.api.ListVirtualKeys(r.Context())
	}()
	wait.Wait()
	data.ResourceSection.Available = usersErr == nil && providersErr == nil && modelsErr == nil
	data.ResourceSection.Count = len(data.Users) + len(data.Providers) + len(data.Models)
	if usersErr != nil || providersErr != nil || modelsErr != nil {
		data.ResourceSection.Error = "Users, providers or models are temporarily unavailable."
	}
	data.KeySection.Available = keysErr == nil
	data.KeySection.Count = len(data.VirtualKeys)
	if keysErr != nil {
		data.KeySection.Error = "Virtual keys are temporarily unavailable."
	}
	s.views.render(w, http.StatusOK, "dashboard", data)
}

func formString(r *http.Request, key string) string {
	return strings.TrimSpace(r.PostForm.Get(key))
}

func statusValue(value string) string {
	return strings.TrimSpace(value)
}
