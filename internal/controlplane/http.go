package controlplane

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Albe83/gwai/internal/platform"
)

type adminHTTP struct {
	adminToken string
	maxBody    int64
	logger     *slog.Logger
	service    string
}

func (h *adminHTTP) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := platform.BearerToken(r.Header.Get("Authorization"))
		if !ok || !platform.SecureEqual(token, h.adminToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="gwai-control-plane"`)
			platform.WriteProblem(w, r, http.StatusUnauthorized, "Unauthorized", "a valid control-plane admin token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *adminHTTP) health(w http.ResponseWriter, _ *http.Request) {
	platform.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *adminHTTP) decode(w http.ResponseWriter, r *http.Request, destination any) bool {
	if err := platform.DecodeJSON(r, destination, h.maxBody, true); err != nil {
		platform.WriteProblem(w, r, http.StatusBadRequest, "Invalid request", err.Error())
		return false
	}
	return true
}

func (h *adminHTTP) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		platform.WriteProblem(w, r, http.StatusBadRequest, "Invalid request", validation.Error())
	case errors.Is(err, ErrNotFound):
		platform.WriteProblem(w, r, http.StatusNotFound, "Not found", "the requested resource does not exist")
	case errors.Is(err, ErrConflict):
		platform.WriteProblem(w, r, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, ErrUnauthorized):
		platform.WriteProblem(w, r, http.StatusUnauthorized, "Unauthorized", "authentication failed")
	case errors.Is(err, ErrForbidden):
		platform.WriteProblem(w, r, http.StatusForbidden, "Forbidden", "access to the requested resource is denied")
	case errors.Is(err, ErrUnavailable):
		h.logger.Error(h.service+" dependency unavailable", "request_id", platform.RequestID(r.Context()), "error", err)
		platform.WriteProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "a required control-plane dependency is unavailable")
	default:
		h.logger.Error(h.service+" request failed", "request_id", platform.RequestID(r.Context()), "error", err)
		platform.WriteProblem(w, r, http.StatusInternalServerError, "Internal Server Error", "the control plane could not process the request")
	}
}

func (h *adminHTTP) writeEntity(w http.ResponseWriter, r *http.Request, status int, etagValue, response any) {
	etag, err := entityETag(etagValue)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag)
	platform.JSON(w, status, response)
}

func requestIfMatch(r *http.Request) ifMatchPrecondition {
	values, present := r.Header[http.CanonicalHeaderKey("If-Match")]
	return ifMatchPrecondition{present: present, value: strings.Join(values, ",")}
}

type ResourceHTTPHandler struct {
	*adminHTTP
	service *ResourceService
}

// NewResourceHTTPHandler exposes only users and providers. Virtual-key routes
// deliberately live in the independently deployable virtual-key service.
func NewResourceHTTPHandler(service *ResourceService, adminToken string, maxBody int64, logger *slog.Logger) http.Handler {
	handler := &ResourceHTTPHandler{
		adminHTTP: &adminHTTP{adminToken: adminToken, maxBody: maxBody, logger: logger, service: "resource control-plane"},
		service:   service,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", handler.health)
	mux.HandleFunc("GET /readyz", handler.health)
	mux.Handle("POST /v1/users", handler.admin(http.HandlerFunc(handler.createUser)))
	mux.Handle("GET /v1/users", handler.admin(http.HandlerFunc(handler.listUsers)))
	mux.Handle("GET /v1/users/{id}", handler.admin(http.HandlerFunc(handler.getUser)))
	mux.Handle("PUT /v1/users/{id}", handler.admin(http.HandlerFunc(handler.updateUser)))
	mux.Handle("DELETE /v1/users/{id}", handler.admin(http.HandlerFunc(handler.deleteUser)))
	mux.Handle("POST /v1/providers", handler.admin(http.HandlerFunc(handler.createProvider)))
	mux.Handle("GET /v1/providers", handler.admin(http.HandlerFunc(handler.listProviders)))
	mux.Handle("GET /v1/providers/{id}", handler.admin(http.HandlerFunc(handler.getProvider)))
	mux.Handle("PUT /v1/providers/{id}", handler.admin(http.HandlerFunc(handler.updateProvider)))
	mux.Handle("DELETE /v1/providers/{id}", handler.admin(http.HandlerFunc(handler.deleteProvider)))
	mux.Handle("POST /v1/models", handler.admin(http.HandlerFunc(handler.createModel)))
	mux.Handle("GET /v1/models", handler.admin(http.HandlerFunc(handler.listModels)))
	mux.Handle("GET /v1/models/{id}", handler.admin(http.HandlerFunc(handler.getModel)))
	mux.Handle("PUT /v1/models/{id}", handler.admin(http.HandlerFunc(handler.updateModel)))
	mux.Handle("DELETE /v1/models/{id}", handler.admin(http.HandlerFunc(handler.deleteModel)))
	return platform.HTTPMiddleware(logger, mux)
}

func (h *ResourceHTTPHandler) createUser(w http.ResponseWriter, r *http.Request) {
	var input UserInput
	if !h.decode(w, r, &input) {
		return
	}
	user, err := h.service.CreateUser(r.Context(), input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusCreated, user, user)
}

func (h *ResourceHTTPHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.service.ListUsers(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, map[string]any{"data": users})
}

func (h *ResourceHTTPHandler) getUser(w http.ResponseWriter, r *http.Request) {
	user, err := h.service.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusOK, user, user)
}

func (h *ResourceHTTPHandler) updateUser(w http.ResponseWriter, r *http.Request) {
	var input UserInput
	if !h.decode(w, r, &input) {
		return
	}
	user, err := h.service.updateUser(r.Context(), r.PathValue("id"), input, requestIfMatch(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusOK, user, user)
}

func (h *ResourceHTTPHandler) deleteUser(w http.ResponseWriter, r *http.Request) {
	if err := h.service.deleteUser(r.Context(), r.PathValue("id"), requestIfMatch(r)); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ResourceHTTPHandler) createProvider(w http.ResponseWriter, r *http.Request) {
	var input ProviderInput
	if !h.decode(w, r, &input) {
		return
	}
	provider, err := h.service.CreateProvider(r.Context(), input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusCreated, provider, provider)
}

func (h *ResourceHTTPHandler) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := h.service.ListProviders(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, map[string]any{"data": providers})
}

func (h *ResourceHTTPHandler) getProvider(w http.ResponseWriter, r *http.Request) {
	provider, err := h.service.GetProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusOK, provider, provider)
}

func (h *ResourceHTTPHandler) updateProvider(w http.ResponseWriter, r *http.Request) {
	var input ProviderInput
	if !h.decode(w, r, &input) {
		return
	}
	provider, err := h.service.updateProvider(r.Context(), r.PathValue("id"), input, requestIfMatch(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusOK, provider, provider)
}

func (h *ResourceHTTPHandler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := h.service.deleteProvider(r.Context(), r.PathValue("id"), requestIfMatch(r)); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
