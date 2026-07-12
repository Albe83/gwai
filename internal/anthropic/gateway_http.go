package anthropic

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/dataplane"
	"github.com/Albe83/gwai/internal/ir"
	"github.com/Albe83/gwai/internal/platform"
)

const PublicAPIVersion = "2023-06-01"

type GatewayHTTPHandler struct {
	dispatcher *dataplane.Dispatcher
	maxBody    int64
	logger     *slog.Logger
}

func NewGatewayHTTPHandler(dispatcher *dataplane.Dispatcher, maxBody int64, logger *slog.Logger) http.Handler {
	handler := &GatewayHTTPHandler{dispatcher: dispatcher, maxBody: maxBody, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", handler.health)
	mux.HandleFunc("GET /readyz", handler.health)
	mux.HandleFunc("POST /v1/messages", handler.createMessage)
	return platform.HTTPMiddleware(logger, mux)
}

func (h *GatewayHTTPHandler) health(w http.ResponseWriter, _ *http.Request) {
	platform.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *GatewayHTTPHandler) writeError(w http.ResponseWriter, r *http.Request, status int, errorType, message string) {
	requestID := platform.RequestID(r.Context())
	if requestID != "" {
		w.Header().Set("request-id", requestID)
	}
	response := ErrorResponse{Type: "error", RequestID: requestID}
	response.Error.Type = errorType
	response.Error.Message = message
	platform.JSON(w, status, response)
}

func (h *GatewayHTTPHandler) writeRuntimeError(w http.ResponseWriter, r *http.Request, err error) {
	var translation *ClientTranslationError
	var validation *controlplane.ValidationError
	var daprError *daprhttp.HTTPError
	switch {
	case errors.As(err, &translation):
		h.writeError(w, r, http.StatusBadRequest, "invalid_request_error", translation.Message)
	case errors.As(err, &validation):
		h.writeError(w, r, http.StatusBadRequest, "invalid_request_error", validation.Error())
	case errors.Is(err, controlplane.ErrUnauthorized):
		h.writeError(w, r, http.StatusUnauthorized, "authentication_error", "invalid x-api-key")
	case errors.Is(err, controlplane.ErrForbidden):
		h.writeError(w, r, http.StatusForbidden, "permission_error", "the API key is not allowed to use the requested model")
	case errors.Is(err, controlplane.ErrNotFound):
		h.writeError(w, r, http.StatusNotFound, "not_found_error", "the requested model does not exist")
	case errors.As(err, &daprError) && daprError.StatusCode == http.StatusTooManyRequests:
		h.writeError(w, r, http.StatusTooManyRequests, "rate_limit_error", "the upstream provider rate limit was exceeded")
	default:
		h.logger.Error("Anthropic message failed", "request_id", platform.RequestID(r.Context()), "error", err)
		h.writeError(w, r, http.StatusBadGateway, "api_error", "the gateway could not complete the upstream request")
	}
}

func (h *GatewayHTTPHandler) createMessage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.Header.Get("x-api-key"))
	if token == "" {
		h.writeError(w, r, http.StatusUnauthorized, "authentication_error", "x-api-key header is required")
		return
	}
	version := strings.TrimSpace(r.Header.Get("anthropic-version"))
	if version == "" {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request_error", "anthropic-version header is required")
		return
	}
	if version != PublicAPIVersion {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request_error", "unsupported anthropic-version; use "+PublicAPIVersion)
		return
	}
	if strings.TrimSpace(r.Header.Get("anthropic-beta")) != "" {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request_error", "anthropic-beta features are not supported")
		return
	}

	var request ClientMessageRequest
	if err := platform.DecodeJSON(r, &request, h.maxBody, true); err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if strings.TrimSpace(request.Model) == "" {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	request.Model = strings.TrimSpace(request.Model)
	response, err := h.dispatcher.Generate(r.Context(), token, request.Model, platform.RequestID(r.Context()), func(route controlplane.Route, requestID string) (irRequest ir.Request, err error) {
		return ToIRRequest(request, route, requestID)
	})
	if err != nil {
		h.writeRuntimeError(w, r, err)
		return
	}
	messageID, err := platform.NewID("msg")
	if err != nil {
		h.writeRuntimeError(w, r, err)
		return
	}
	result, err := FromIRResponse(response, request.Model, messageID)
	if err != nil {
		h.writeRuntimeError(w, r, err)
		return
	}
	if requestID := platform.RequestID(r.Context()); requestID != "" {
		w.Header().Set("request-id", requestID)
	}
	platform.JSON(w, http.StatusOK, result)
}
