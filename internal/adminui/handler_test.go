package adminui

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
)

type fakeAPI struct {
	mu                     sync.Mutex
	users                  []controlplane.User
	providers              []controlplane.Provider
	keys                   []controlplane.PublicVirtualKey
	secret                 string
	errors                 map[string]error
	calls                  []string
	lastUserInput          controlplane.UserInput
	lastProviderInput      controlplane.ProviderInput
	lastKeyInput           controlplane.VirtualKeyInput
	lastUserETag           string
	lastProviderETag       string
	lastKeyETag            string
	lastDeleteUserETag     string
	lastDeleteProviderETag string
	lastDeleteKeyETag      string
}

const fakeETag = `"fake-version"`

func (f *fakeAPI) record(name string) error {
	f.calls = append(f.calls, name)
	if f.errors != nil {
		return f.errors[name]
	}
	return nil
}

func (f *fakeAPI) ListUsers(context.Context) ([]controlplane.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ListUsers"); err != nil {
		return nil, err
	}
	return slices.Clone(f.users), nil
}

func (f *fakeAPI) GetUser(_ context.Context, id string) (Versioned[controlplane.User], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("GetUser"); err != nil {
		return Versioned[controlplane.User]{}, err
	}
	for _, value := range f.users {
		if value.ID == id {
			return Versioned[controlplane.User]{Value: value, ETag: fakeETag}, nil
		}
	}
	return Versioned[controlplane.User]{}, &APIError{Status: http.StatusNotFound, Title: "Not found", Detail: "user not found"}
}

func (f *fakeAPI) CreateUser(_ context.Context, input controlplane.UserInput) (controlplane.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("CreateUser"); err != nil {
		return controlplane.User{}, err
	}
	f.lastUserInput = input
	value := controlplane.User{ID: "usr_created", Name: input.Name, Email: input.Email, Status: input.Status, Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.users = append(f.users, value)
	return value, nil
}

func (f *fakeAPI) UpdateUser(_ context.Context, id string, input controlplane.UserInput, etag string) (Versioned[controlplane.User], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("UpdateUser"); err != nil {
		return Versioned[controlplane.User]{}, err
	}
	f.lastUserInput = input
	f.lastUserETag = etag
	for index := range f.users {
		if f.users[index].ID == id {
			f.users[index].Name, f.users[index].Email, f.users[index].Status = input.Name, input.Email, input.Status
			f.users[index].Revision++
			return Versioned[controlplane.User]{Value: f.users[index], ETag: fakeETag}, nil
		}
	}
	return Versioned[controlplane.User]{}, &APIError{Status: http.StatusNotFound, Title: "Not found"}
}

func (f *fakeAPI) DeleteUser(_ context.Context, id, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("DeleteUser"); err != nil {
		return err
	}
	f.lastDeleteUserETag = etag
	f.users = slices.DeleteFunc(f.users, func(value controlplane.User) bool { return value.ID == id })
	return nil
}

func (f *fakeAPI) ListProviders(context.Context) ([]controlplane.Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ListProviders"); err != nil {
		return nil, err
	}
	return slices.Clone(f.providers), nil
}

func (f *fakeAPI) GetProvider(_ context.Context, id string) (Versioned[controlplane.Provider], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("GetProvider"); err != nil {
		return Versioned[controlplane.Provider]{}, err
	}
	for _, value := range f.providers {
		if value.ID == id {
			return Versioned[controlplane.Provider]{Value: value, ETag: fakeETag}, nil
		}
	}
	return Versioned[controlplane.Provider]{}, &APIError{Status: http.StatusNotFound, Title: "Not found"}
}

func (f *fakeAPI) CreateProvider(_ context.Context, input controlplane.ProviderInput) (controlplane.Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("CreateProvider"); err != nil {
		return controlplane.Provider{}, err
	}
	f.lastProviderInput = input
	value := controlplane.Provider{ID: "prv_created", Slug: input.Slug, Name: input.Name, Kind: input.Kind, BaseURL: input.BaseURL, APIVersion: input.APIVersion, AdapterAppID: input.AdapterAppID, SecretRef: input.SecretRef, Status: input.Status}
	f.providers = append(f.providers, value)
	return value, nil
}

func (f *fakeAPI) UpdateProvider(_ context.Context, id string, input controlplane.ProviderInput, etag string) (Versioned[controlplane.Provider], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("UpdateProvider"); err != nil {
		return Versioned[controlplane.Provider]{}, err
	}
	f.lastProviderInput = input
	f.lastProviderETag = etag
	for index := range f.providers {
		if f.providers[index].ID == id {
			current := &f.providers[index]
			current.Name, current.Kind, current.BaseURL, current.APIVersion, current.SecretRef, current.Status = input.Name, input.Kind, input.BaseURL, input.APIVersion, input.SecretRef, input.Status
			return Versioned[controlplane.Provider]{Value: *current, ETag: fakeETag}, nil
		}
	}
	return Versioned[controlplane.Provider]{}, &APIError{Status: http.StatusNotFound, Title: "Not found"}
}

func (f *fakeAPI) DeleteProvider(_ context.Context, id, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("DeleteProvider"); err != nil {
		return err
	}
	f.lastDeleteProviderETag = etag
	f.providers = slices.DeleteFunc(f.providers, func(value controlplane.Provider) bool { return value.ID == id })
	return nil
}

func (f *fakeAPI) ListVirtualKeys(context.Context) ([]controlplane.PublicVirtualKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ListVirtualKeys"); err != nil {
		return nil, err
	}
	return slices.Clone(f.keys), nil
}

func (f *fakeAPI) GetVirtualKey(_ context.Context, id string) (Versioned[controlplane.PublicVirtualKey], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("GetVirtualKey"); err != nil {
		return Versioned[controlplane.PublicVirtualKey]{}, err
	}
	for _, value := range f.keys {
		if value.ID == id {
			return Versioned[controlplane.PublicVirtualKey]{Value: value, ETag: fakeETag}, nil
		}
	}
	return Versioned[controlplane.PublicVirtualKey]{}, &APIError{Status: http.StatusNotFound, Title: "Not found"}
}

func (f *fakeAPI) CreateVirtualKey(_ context.Context, input controlplane.VirtualKeyInput) (controlplane.CreatedVirtualKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("CreateVirtualKey"); err != nil {
		return controlplane.CreatedVirtualKey{}, err
	}
	f.lastKeyInput = input
	value := controlplane.PublicVirtualKey{ID: "key_created", Name: input.Name, UserID: input.UserID, Prefix: "gwai_created", AllowedModels: input.AllowedModels, Status: input.Status, ExpiresAt: input.ExpiresAt}
	f.keys = append(f.keys, value)
	return controlplane.CreatedVirtualKey{VirtualKey: value, Key: f.secret}, nil
}

func (f *fakeAPI) UpdateVirtualKey(_ context.Context, id string, input controlplane.VirtualKeyInput, etag string) (Versioned[controlplane.PublicVirtualKey], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("UpdateVirtualKey"); err != nil {
		return Versioned[controlplane.PublicVirtualKey]{}, err
	}
	f.lastKeyInput = input
	f.lastKeyETag = etag
	for index := range f.keys {
		if f.keys[index].ID == id {
			current := &f.keys[index]
			current.Name, current.UserID, current.AllowedModels, current.Status, current.ExpiresAt = input.Name, input.UserID, input.AllowedModels, input.Status, input.ExpiresAt
			return Versioned[controlplane.PublicVirtualKey]{Value: *current, ETag: fakeETag}, nil
		}
	}
	return Versioned[controlplane.PublicVirtualKey]{}, &APIError{Status: http.StatusNotFound, Title: "Not found"}
}

func (f *fakeAPI) DeleteVirtualKey(_ context.Context, id, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("DeleteVirtualKey"); err != nil {
		return err
	}
	f.lastDeleteKeyETag = etag
	f.keys = slices.DeleteFunc(f.keys, func(value controlplane.PublicVirtualKey) bool { return value.ID == id })
	return nil
}

func newTestHandler(t *testing.T, api *fakeAPI, now func() time.Time, secure bool) http.Handler {
	t.Helper()
	handler, err := NewHandler(api, Config{
		AdminToken: "cluster-admin-token", SessionTTL: 2 * time.Minute,
		CookieSecure: secure, Now: now,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

var csrfPattern = regexp.MustCompile(`name="_csrf" value="([A-Za-z0-9_.-]+)"`)
var operationPattern = regexp.MustCompile(`name="_operation" value="([A-Za-z0-9_-]+)"`)

func csrfFromBody(t *testing.T, body string) string {
	t.Helper()
	match := csrfPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("CSRF token not found in body: %s", body)
	}
	return match[1]
}

func operationFromBody(t *testing.T, body string) string {
	t.Helper()
	match := operationPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("operation token not found in body: %s", body)
	}
	return match[1]
}

func sessionCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == defaultCookieName {
			return cookie
		}
	}
	t.Fatal("session cookie not found")
	return nil
}

func request(handler http.Handler, method, path string, cookie *http.Cookie, values url.Values) *httptest.ResponseRecorder {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if values != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func login(t *testing.T, handler http.Handler) (*http.Cookie, string) {
	t.Helper()
	page := request(handler, http.MethodGet, "/login", nil, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("login page returned %d", page.Code)
	}
	preauth := sessionCookie(t, page)
	csrf := csrfFromBody(t, page.Body.String())
	response := request(handler, http.MethodPost, "/login", preauth, url.Values{"_csrf": {csrf}, "admin_token": {"cluster-admin-token"}})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login returned %d: %s", response.Code, response.Body.String())
	}
	auth := sessionCookie(t, response)
	dashboard := request(handler, http.MethodGet, "/", auth, nil)
	if dashboard.Code != http.StatusOK {
		t.Fatalf("dashboard returned %d: %s", dashboard.Code, dashboard.Body.String())
	}
	return auth, csrfFromBody(t, dashboard.Body.String())
}

func TestAuthenticationSessionCSRFAndSecurityHeaders(t *testing.T) {
	api := &fakeAPI{}
	handler := newTestHandler(t, api, time.Now, true)

	redirect := request(handler, http.MethodGet, "/", nil, nil)
	if redirect.Code != http.StatusSeeOther || redirect.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated response: %d %q", redirect.Code, redirect.Header().Get("Location"))
	}
	if redirect.Header().Get("Content-Security-Policy") == "" || redirect.Header().Get("X-Frame-Options") != "DENY" || redirect.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("security headers are incomplete")
	}

	page := request(handler, http.MethodGet, "/login", nil, nil)
	preauth := sessionCookie(t, page)
	if !preauth.HttpOnly || !preauth.Secure || preauth.SameSite != http.SameSiteStrictMode || preauth.MaxAge <= 0 {
		t.Fatalf("insecure session cookie: %+v", preauth)
	}
	if strings.Contains(page.Body.String(), "cluster-admin-token") {
		t.Fatal("admin token leaked into login page")
	}

	missingCSRF := request(handler, http.MethodPost, "/login", preauth, url.Values{"admin_token": {"cluster-admin-token"}})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing login CSRF returned %d", missingCSRF.Code)
	}
	csrf := csrfFromBody(t, page.Body.String())
	wrong := request(handler, http.MethodPost, "/login", preauth, url.Values{"_csrf": {csrf}, "admin_token": {"wrong-secret"}})
	if wrong.Code != http.StatusUnauthorized || strings.Contains(wrong.Body.String(), "wrong-secret") {
		t.Fatalf("unexpected invalid-login response: %d %s", wrong.Code, wrong.Body.String())
	}

	response := request(handler, http.MethodPost, "/login", preauth, url.Values{"_csrf": {csrf}, "admin_token": {"cluster-admin-token"}})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("valid login returned %d", response.Code)
	}
	auth := sessionCookie(t, response)
	if auth.Value == preauth.Value {
		t.Fatal("session ID was not rotated at authentication")
	}
	dashboard := request(handler, http.MethodGet, "/", auth, nil)
	authCSRF := csrfFromBody(t, dashboard.Body.String())

	badLogout := request(handler, http.MethodPost, "/logout", auth, url.Values{})
	if badLogout.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF returned %d", badLogout.Code)
	}
	if stillLoggedIn := request(handler, http.MethodGet, "/", auth, nil); stillLoggedIn.Code != http.StatusOK {
		t.Fatalf("bad CSRF destroyed session: %d", stillLoggedIn.Code)
	}
	logout := request(handler, http.MethodPost, "/logout", auth, url.Values{"_csrf": {authCSRF}})
	if logout.Code != http.StatusSeeOther || sessionCookie(t, logout).MaxAge != -1 {
		t.Fatalf("logout did not expire cookie: %d", logout.Code)
	}
	if after := request(handler, http.MethodGet, "/", auth, nil); after.Code != http.StatusSeeOther {
		t.Fatalf("destroyed session remained valid: %d", after.Code)
	}

	asset := request(handler, http.MethodGet, "/assets/styles.css", nil, nil)
	if asset.Code != http.StatusOK || !strings.HasPrefix(asset.Header().Get("Cache-Control"), "public") || !strings.Contains(asset.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("asset response invalid: %d headers=%v", asset.Code, asset.Header())
	}
	unknown := request(handler, http.MethodGet, "/not-a-real-page", auth, nil)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown page returned %d instead of 404", unknown.Code)
	}
}

func TestAnonymousLoginChallengesDoNotConsumeSessionCapacity(t *testing.T) {
	handler := newTestHandler(t, &fakeAPI{}, time.Now, false)
	var page *httptest.ResponseRecorder
	for range defaultMaxSessions + 16 {
		page = request(handler, http.MethodGet, "/login", nil, nil)
		if page.Code != http.StatusOK {
			t.Fatalf("anonymous login challenge exhausted capacity at status %d", page.Code)
		}
	}
	challenge := sessionCookie(t, page)
	response := request(handler, http.MethodPost, "/login", challenge, url.Values{
		"_csrf": {csrfFromBody(t, page.Body.String())}, "admin_token": {"cluster-admin-token"},
	})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("valid login after anonymous flood returned %d", response.Code)
	}
}

func TestLoginChallengeKeyIsIndependentFromAdminCredential(t *testing.T) {
	fixedNow := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	challengeFor := func(adminToken string) string {
		t.Helper()
		random := bytes.NewReader(bytes.Repeat([]byte{0x5a}, loginChallengeKeySize+loginChallengeNonceSize))
		handler, err := NewHandler(&fakeAPI{}, Config{
			AdminToken: adminToken, SessionTTL: time.Minute, Random: random, Now: func() time.Time { return fixedNow },
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		page := request(handler, http.MethodGet, "/login", nil, nil)
		if page.Code != http.StatusOK {
			t.Fatalf("login page returned %d", page.Code)
		}
		return csrfFromBody(t, page.Body.String())
	}

	if weak, strong := challengeFor("weak"), challengeFor("different-administrator-token"); weak != strong {
		t.Fatal("login challenge signature depends on the administrator credential")
	}
}

func TestHandlerRejectsUnsafeConfiguration(t *testing.T) {
	api := &fakeAPI{}
	for _, test := range []Config{
		{AdminToken: " token-with-space", SessionTTL: time.Minute},
		{AdminToken: "token", SessionTTL: time.Minute, CookieName: "bad=name"},
		{AdminToken: "token", SessionTTL: time.Minute, RequestTimeout: -time.Second},
	} {
		if _, err := NewHandler(api, test, nil); err == nil {
			t.Fatalf("unsafe config was accepted: %+v", test)
		}
	}
}

func TestSessionExpiresAndMutationRequiresCSRF(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	api := &fakeAPI{}
	handler := newTestHandler(t, api, func() time.Time { return now }, false)
	cookie, _ := login(t, handler)

	api.mu.Lock()
	callsBefore := len(api.calls)
	api.mu.Unlock()
	withoutCSRF := request(handler, http.MethodPost, "/users", cookie, url.Values{"name": {"Ada"}, "email": {"ada@example.com"}, "status": {"active"}})
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("mutation without CSRF returned %d", withoutCSRF.Code)
	}
	api.mu.Lock()
	if slices.Contains(api.calls[callsBefore:], "CreateUser") {
		t.Fatal("CSRF failure reached API")
	}
	api.mu.Unlock()

	now = now.Add(3 * time.Minute)
	expired := request(handler, http.MethodGet, "/users", cookie, nil)
	if expired.Code != http.StatusSeeOther || expired.Header().Get("Location") != "/login" {
		t.Fatalf("expired session returned %d", expired.Code)
	}
}

func TestCompleteCRUDLifecycleAndOneTimeReveal(t *testing.T) {
	api := &fakeAPI{
		users:     []controlplane.User{{ID: "usr_one", Name: "Ada", Email: "ada@example.com", Status: controlplane.StatusActive, Revision: 1}},
		providers: []controlplane.Provider{{ID: "prv_one", Slug: "anthropic", Name: "Anthropic", Kind: controlplane.ProviderKindAnthropic, BaseURL: "https://api.anthropic.com", APIVersion: "2023-06-01", AdapterAppID: "gwai-anthropic", Status: controlplane.StatusActive}},
		secret:    "gwai_super_secret_once",
	}
	handler := newTestHandler(t, api, time.Now, false)
	cookie, csrf := login(t, handler)

	createdUser := request(handler, http.MethodPost, "/users", cookie, url.Values{"_csrf": {csrf}, "name": {" Grace "}, "email": {" grace@example.com "}, "status": {"active"}})
	if createdUser.Code != http.StatusSeeOther || api.lastUserInput.Name != "Grace" || api.lastUserInput.Email != "grace@example.com" {
		t.Fatalf("user create failed: %d input=%+v", createdUser.Code, api.lastUserInput)
	}
	userStatus := request(handler, http.MethodPost, "/users/usr_one/status", cookie, url.Values{"_csrf": {csrf}, "_etag": {fakeETag}, "status": {"disabled"}})
	if userStatus.Code != http.StatusSeeOther || api.lastUserInput.Name != "Ada" || api.lastUserInput.Status != controlplane.StatusDisabled {
		t.Fatalf("user lifecycle lost replacement fields: %+v", api.lastUserInput)
	}
	userUpdate := request(handler, http.MethodPost, "/users/usr_one", cookie, url.Values{"_csrf": {csrf}, "_etag": {fakeETag}, "name": {"Ada Lovelace"}, "email": {"ada@example.com"}, "status": {"active"}})
	if userUpdate.Code != http.StatusSeeOther || api.lastUserInput.Name != "Ada Lovelace" {
		t.Fatalf("user update failed: %d", userUpdate.Code)
	}
	if api.lastUserETag != fakeETag {
		t.Fatalf("user update omitted If-Match: %q", api.lastUserETag)
	}

	providerValues := url.Values{"_csrf": {csrf}, "slug": {"openai"}, "name": {"OpenAI"}, "kind": {"openai-responses"}, "base_url": {"https://api.openai.com"}, "api_version": {"v1"}, "adapter_app_id": {"gwai-openai"}, "secret_store": {"kubernetes"}, "secret_name": {"openai-secret"}, "secret_key": {"api-key"}, "status": {"active"}}
	createdProvider := request(handler, http.MethodPost, "/providers", cookie, providerValues)
	if createdProvider.Code != http.StatusSeeOther || api.lastProviderInput.SecretRef.Name != "openai-secret" {
		t.Fatalf("provider create failed: %d input=%+v", createdProvider.Code, api.lastProviderInput)
	}
	providerUpdateValues := url.Values{"_csrf": {csrf}, "_etag": {fakeETag}, "slug": {"anthropic"}, "name": {"Anthropic updated"}, "kind": {"anthropic"}, "base_url": {"https://api.anthropic.com"}, "api_version": {"2023-06-01"}, "adapter_app_id": {"gwai-anthropic"}, "secret_store": {"kubernetes"}, "secret_name": {"anthropic-secret"}, "secret_key": {"api-key"}, "status": {"active"}}
	providerUpdate := request(handler, http.MethodPost, "/providers/prv_one", cookie, providerUpdateValues)
	if providerUpdate.Code != http.StatusSeeOther || api.lastProviderInput.Name != "Anthropic updated" || api.lastProviderInput.Slug != "anthropic" {
		t.Fatalf("provider update failed: %d input=%+v", providerUpdate.Code, api.lastProviderInput)
	}
	if api.lastProviderETag != fakeETag {
		t.Fatalf("provider update omitted If-Match: %q", api.lastProviderETag)
	}
	providerStatus := request(handler, http.MethodPost, "/providers/prv_one/status", cookie, url.Values{"_csrf": {csrf}, "_etag": {fakeETag}, "status": {"disabled"}})
	if providerStatus.Code != http.StatusSeeOther || api.lastProviderInput.Slug != "anthropic" || api.lastProviderInput.AdapterAppID != "gwai-anthropic" || api.lastProviderInput.Status != controlplane.StatusDisabled {
		t.Fatalf("provider lifecycle lost immutable fields: %+v", api.lastProviderInput)
	}

	keyForm := request(handler, http.MethodGet, "/virtual-keys/new", cookie, nil)
	keyValues := url.Values{"_csrf": {csrf}, "_operation": {operationFromBody(t, keyForm.Body.String())}, "name": {"CLI"}, "user_id": {"usr_one"}, "allowed_models": {"anthropic/claude\nanthropic/claude"}, "status": {"active"}}
	createdKey := request(handler, http.MethodPost, "/virtual-keys", cookie, keyValues)
	if createdKey.Code != http.StatusCreated || !strings.Contains(createdKey.Body.String(), api.secret) || !strings.Contains(createdKey.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("key creation did not return the one-time secret safely: %d body=%q", createdKey.Code, createdKey.Body.String())
	}
	if len(api.lastKeyInput.AllowedModels) != 1 {
		t.Fatalf("models were not normalized: %+v", api.lastKeyInput.AllowedModels)
	}
	secondSubmit := request(handler, http.MethodPost, "/virtual-keys", cookie, keyValues)
	if secondSubmit.Code != http.StatusConflict || strings.Contains(secondSubmit.Body.String(), api.secret) {
		t.Fatalf("key creation form was reusable: %d", secondSubmit.Code)
	}
	keyUpdate := request(handler, http.MethodPost, "/virtual-keys/key_created", cookie, url.Values{"_csrf": {csrf}, "_etag": {fakeETag}, "name": {"CLI updated"}, "user_id": {"usr_one"}, "allowed_models": {"anthropic/claude-v2"}, "status": {"active"}})
	if keyUpdate.Code != http.StatusSeeOther || api.lastKeyInput.Name != "CLI updated" || len(api.lastKeyInput.AllowedModels) != 1 {
		t.Fatalf("key update failed: %d input=%+v", keyUpdate.Code, api.lastKeyInput)
	}
	if api.lastKeyETag != fakeETag {
		t.Fatalf("key update omitted If-Match: %q", api.lastKeyETag)
	}

	keyStatus := request(handler, http.MethodPost, "/virtual-keys/key_created/status", cookie, url.Values{"_csrf": {csrf}, "_etag": {fakeETag}, "status": {"disabled"}})
	if keyStatus.Code != http.StatusSeeOther || api.lastKeyInput.Name != "CLI updated" || api.lastKeyInput.UserID != "usr_one" || api.lastKeyInput.Status != controlplane.StatusDisabled {
		t.Fatalf("key lifecycle lost replacement fields: %+v", api.lastKeyInput)
	}
	keyDeletePage := request(handler, http.MethodGet, "/virtual-keys/key_created/delete", cookie, nil)
	if keyDeletePage.Code != http.StatusOK || !strings.Contains(keyDeletePage.Body.String(), "Delete virtual key") {
		t.Fatalf("key delete confirmation missing: %d", keyDeletePage.Code)
	}
	if response := request(handler, http.MethodPost, "/virtual-keys/key_created/delete", cookie, url.Values{"_csrf": {csrf}, "_etag": {fakeETag}}); response.Code != http.StatusSeeOther {
		t.Fatalf("key delete failed: %d", response.Code)
	}
	if response := request(handler, http.MethodPost, "/providers/prv_one/delete", cookie, url.Values{"_csrf": {csrf}, "_etag": {fakeETag}}); response.Code != http.StatusSeeOther {
		t.Fatalf("provider delete failed: %d", response.Code)
	}
	if response := request(handler, http.MethodPost, "/users/usr_one/delete", cookie, url.Values{"_csrf": {csrf}, "_etag": {fakeETag}}); response.Code != http.StatusSeeOther {
		t.Fatalf("user delete failed: %d", response.Code)
	}
	if api.lastDeleteUserETag != fakeETag || api.lastDeleteProviderETag != fakeETag || api.lastDeleteKeyETag != fakeETag {
		t.Fatalf("conditional delete versions were lost: user=%q provider=%q key=%q", api.lastDeleteUserETag, api.lastDeleteProviderETag, api.lastDeleteKeyETag)
	}
}

func TestStatusAndDeleteConfirmationsAreVersionBound(t *testing.T) {
	api := &fakeAPI{
		users:     []controlplane.User{{ID: "usr_one", Name: "Ada", Email: "ada@example.com", Status: controlplane.StatusActive}},
		providers: []controlplane.Provider{{ID: "prv_one", Slug: "anthropic", Name: "Anthropic", Kind: controlplane.ProviderKindAnthropic, AdapterAppID: "gwai-anthropic", Status: controlplane.StatusActive}},
		keys:      []controlplane.PublicVirtualKey{{ID: "key_one", Name: "CLI", UserID: "usr_one", Status: controlplane.StatusActive}},
	}
	handler := newTestHandler(t, api, time.Now, false)
	cookie, csrf := login(t, handler)

	for _, path := range []string{
		"/users/usr_one/status?to=disabled", "/providers/prv_one/status?to=disabled", "/virtual-keys/key_one/status?to=disabled",
		"/users/usr_one/delete", "/providers/prv_one/delete", "/virtual-keys/key_one/delete",
	} {
		page := request(handler, http.MethodGet, path, cookie, nil)
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="_etag" value="&#34;fake-version&#34;"`) {
			t.Fatalf("confirmation %s did not carry its resource version: %d %s", path, page.Code, page.Body.String())
		}
	}

	before := len(api.calls)
	for _, operation := range []struct{ path, status string }{
		{"/users/usr_one/status", "disabled"},
		{"/providers/prv_one/status", "disabled"},
		{"/virtual-keys/key_one/status", "disabled"},
		{"/users/usr_one/delete", ""},
		{"/providers/prv_one/delete", ""},
		{"/virtual-keys/key_one/delete", ""},
	} {
		values := url.Values{"_csrf": {csrf}}
		if operation.status != "" {
			values.Set("status", operation.status)
		}
		response := request(handler, http.MethodPost, operation.path, cookie, values)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("versionless confirmation %s returned %d", operation.path, response.Code)
		}
	}
	if len(api.calls) != before {
		t.Fatal("a versionless lifecycle confirmation reached the control plane")
	}
}

func TestAmbiguousVirtualKeyCreationDoesNotOfferImmediateRetry(t *testing.T) {
	api := &fakeAPI{errors: map[string]error{
		"CreateVirtualKey": &APIError{Status: http.StatusBadGateway, Title: "Unavailable", Detail: "request failed", Ambiguous: true},
	}}
	handler := newTestHandler(t, api, time.Now, false)
	cookie, csrf := login(t, handler)
	form := request(handler, http.MethodGet, "/virtual-keys/new", cookie, nil)
	response := request(handler, http.MethodPost, "/virtual-keys", cookie, url.Values{
		"_csrf": {csrf}, "_operation": {operationFromBody(t, form.Body.String())},
		"name": {"possibly created"}, "user_id": {"usr_one"}, "status": {"active"},
	})
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "outcome unknown") || !strings.Contains(response.Body.String(), "Inspect the virtual-key list") {
		t.Fatalf("ambiguous key outcome was not handled safely: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `name="_operation"`) {
		t.Fatal("ambiguous key outcome offered an immediate replacement form")
	}
}

func TestErrorsAndUntrustedValuesAreSafelyRendered(t *testing.T) {
	api := &fakeAPI{users: []controlplane.User{{ID: "usr_x", Name: `<script>alert("x")</script>`, Email: "x@example.com", Status: controlplane.StatusActive}}, errors: map[string]error{}}
	handler := newTestHandler(t, api, time.Now, false)
	cookie, csrf := login(t, handler)
	page := request(handler, http.MethodGet, "/users", cookie, nil)
	if strings.Contains(page.Body.String(), `<script>alert`) || !strings.Contains(page.Body.String(), "&lt;script&gt;") {
		t.Fatalf("template did not escape untrusted value: %s", page.Body.String())
	}

	api.mu.Lock()
	api.errors["DeleteUser"] = &APIError{Status: http.StatusConflict, Title: "Conflict", Detail: "user still has virtual keys", Instance: "req_safe"}
	api.mu.Unlock()
	response := request(handler, http.MethodPost, "/users/usr_x/delete", cookie, url.Values{"_csrf": {csrf}, "_etag": {fakeETag}})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "user still has virtual keys") || !strings.Contains(response.Body.String(), "req_safe") {
		t.Fatalf("problem response not rendered: %d %s", response.Code, response.Body.String())
	}
}

func TestVirtualKeyExpiryRoundTripPreservesSubsecondPrecision(t *testing.T) {
	expires := time.Date(2026, 7, 12, 12, 34, 59, 123456789, time.UTC)
	form := virtualKeyFormFromModel(controlplane.PublicVirtualKey{ExpiresAt: &expires})
	input, err := form.input()
	if err != nil {
		t.Fatal(err)
	}
	if input.ExpiresAt == nil || !input.ExpiresAt.Equal(expires) {
		t.Fatalf("expiry round-trip = %v, want %v (form %q)", input.ExpiresAt, expires, form.ExpiresAt)
	}
}
