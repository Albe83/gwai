package controlplane

import (
	"net/http"

	"github.com/Albe83/gwai/internal/platform"
)

func (h *ResourceHTTPHandler) createModel(w http.ResponseWriter, r *http.Request) {
	var input ModelInput
	if !h.decode(w, r, &input) {
		return
	}
	model, err := h.service.CreateModel(r.Context(), input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusCreated, model, model)
}

func (h *ResourceHTTPHandler) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.service.ListModels(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, map[string]any{"data": models})
}

func (h *ResourceHTTPHandler) getModel(w http.ResponseWriter, r *http.Request) {
	model, err := h.service.GetModel(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusOK, model, model)
}

func (h *ResourceHTTPHandler) updateModel(w http.ResponseWriter, r *http.Request) {
	var input ModelInput
	if !h.decode(w, r, &input) {
		return
	}
	model, err := h.service.updateModel(r.Context(), r.PathValue("id"), input, requestIfMatch(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeEntity(w, r, http.StatusOK, model, model)
}

func (h *ResourceHTTPHandler) deleteModel(w http.ResponseWriter, r *http.Request) {
	if err := h.service.deleteModel(r.Context(), r.PathValue("id"), requestIfMatch(r)); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
