package controlplane

import (
	"log/slog"
	"net/http"

	"github.com/Albe83/gwai/internal/platform"
)

type VirtualKeyHTTPHandler struct {
	*adminHTTP
	service  *VirtualKeyService
	appToken string
}

func NewVirtualKeyHTTPHandler(service *VirtualKeyService, adminToken, appToken string, maxBody int64, logger *slog.Logger) http.Handler {
	handler := &VirtualKeyHTTPHandler{
		adminHTTP: &adminHTTP{adminToken: adminToken, maxBody: maxBody, logger: logger, service: "virtual-key control-plane"},
		service:   service,
		appToken:  appToken,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", handler.health)
	mux.HandleFunc("GET /readyz", handler.health)
	mux.Handle("POST /v1/virtual-keys", handler.admin(http.HandlerFunc(handler.createVirtualKey)))
	mux.Handle("GET /v1/virtual-keys", handler.admin(http.HandlerFunc(handler.listVirtualKeys)))
	mux.Handle("GET /v1/virtual-keys/{id}", handler.admin(http.HandlerFunc(handler.getVirtualKey)))
	mux.Handle("PUT /v1/virtual-keys/{id}", handler.admin(http.HandlerFunc(handler.updateVirtualKey)))
	mux.Handle("DELETE /v1/virtual-keys/{id}", handler.admin(http.HandlerFunc(handler.deleteVirtualKey)))
	mux.Handle("POST /internal/v1/subjects/sync", handler.internal(http.HandlerFunc(handler.syncSubject)))
	mux.Handle("POST /internal/v1/subjects/fence", handler.internal(http.HandlerFunc(handler.fenceSubject)))
	return platform.HTTPMiddleware(logger, mux)
}

func (h *VirtualKeyHTTPHandler) internal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.appToken == "" || !platform.SecureEqual(r.Header.Get("dapr-api-token"), h.appToken) {
			platform.WriteProblem(w, r, http.StatusUnauthorized, "Unauthorized", "the endpoint is only available through the authenticated Dapr sidecar")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *VirtualKeyHTTPHandler) createVirtualKey(w http.ResponseWriter, r *http.Request) {
	var input VirtualKeyInput
	if !h.decode(w, r, &input) {
		return
	}
	key, err := h.service.CreateVirtualKey(r.Context(), input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// The creation envelope contains a one-time secret; its validator tracks the
	// persistent public resource so it can be reused by a later conditional PUT.
	h.writeEntity(w, r, http.StatusCreated, key.VirtualKey, key)
}

func (h *VirtualKeyHTTPHandler) listVirtualKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.service.ListVirtualKeys(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, map[string]any{"data": keys})
}

func (h *VirtualKeyHTTPHandler) getVirtualKey(w http.ResponseWriter, r *http.Request) {
	key, err := h.service.GetVirtualKey(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusOK, key, key)
}

func (h *VirtualKeyHTTPHandler) updateVirtualKey(w http.ResponseWriter, r *http.Request) {
	var input VirtualKeyInput
	if !h.decode(w, r, &input) {
		return
	}
	key, err := h.service.updateVirtualKey(r.Context(), r.PathValue("id"), input, requestIfMatch(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusOK, key, key)
}

func (h *VirtualKeyHTTPHandler) deleteVirtualKey(w http.ResponseWriter, r *http.Request) {
	if err := h.service.deleteVirtualKey(r.Context(), r.PathValue("id"), requestIfMatch(r)); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *VirtualKeyHTTPHandler) syncSubject(w http.ResponseWriter, r *http.Request) {
	var subject KeySubject
	if !h.decode(w, r, &subject) {
		return
	}
	if err := h.service.SyncSubject(r.Context(), subject); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *VirtualKeyHTTPHandler) fenceSubject(w http.ResponseWriter, r *http.Request) {
	var subject KeySubject
	if !h.decode(w, r, &subject) {
		return
	}
	if err := h.service.FenceSubject(r.Context(), subject); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
