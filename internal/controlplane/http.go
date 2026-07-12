package controlplane

import (
	"errors"
	"log/slog"
	"net/http"

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
	platform.JSON(w, http.StatusCreated, user)
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
	platform.JSON(w, http.StatusOK, user)
}

func (h *ResourceHTTPHandler) updateUser(w http.ResponseWriter, r *http.Request) {
	var input UserInput
	if !h.decode(w, r, &input) {
		return
	}
	user, err := h.service.UpdateUser(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, user)
}

func (h *ResourceHTTPHandler) deleteUser(w http.ResponseWriter, r *http.Request) {
	if err := h.service.DeleteUser(r.Context(), r.PathValue("id")); err != nil {
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
	platform.JSON(w, http.StatusCreated, provider)
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
	platform.JSON(w, http.StatusOK, provider)
}

func (h *ResourceHTTPHandler) updateProvider(w http.ResponseWriter, r *http.Request) {
	var input ProviderInput
	if !h.decode(w, r, &input) {
		return
	}
	provider, err := h.service.UpdateProvider(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, provider)
}

func (h *ResourceHTTPHandler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := h.service.DeleteProvider(r.Context(), r.PathValue("id")); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
