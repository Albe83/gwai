package openairesponses

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/dataplane"
	"github.com/Albe83/gwai/internal/ir"
	"github.com/Albe83/gwai/internal/platform"
)

type GatewayHandler struct {
	dispatcher *dataplane.Dispatcher
	maxBody    int64
	logger     *slog.Logger
	now        func() time.Time
}

func NewGatewayHTTPHandler(runtime dataplane.Runtime, invoker dataplane.Invoker, maxBody int64, requestTimeout time.Duration, logger *slog.Logger) http.Handler {
	handler := &GatewayHandler{
		dispatcher: dataplane.NewDispatcher(runtime, invoker, requestTimeout),
		maxBody:    maxBody, logger: logger, now: func() time.Time { return time.Now().UTC() },
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", handler.health)
	mux.HandleFunc("GET /readyz", handler.health)
	mux.HandleFunc("POST /v1/responses", handler.createResponse)
	return platform.HTTPMiddleware(logger, mux)
}

func (h *GatewayHandler) health(w http.ResponseWriter, _ *http.Request) {
	platform.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *GatewayHandler) writeAPIError(w http.ResponseWriter, status int, errorType, code, message, param string) {
	var parameter *string
	if param != "" {
		parameter = &param
	}
	platform.JSON(w, status, ErrorResponse{Error: APIError{Message: message, Type: errorType, Param: parameter, Code: code}})
}

func (h *GatewayHandler) writeRuntimeError(w http.ResponseWriter, r *http.Request, err error) {
	var translation *TranslationError
	var validation *controlplane.ValidationError
	var daprError *daprhttp.HTTPError
	switch {
	case errors.As(err, &translation):
		h.writeAPIError(w, http.StatusBadRequest, "invalid_request_error", translation.Code, translation.Message, translation.Param)
	case errors.As(err, &validation):
		h.writeAPIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_model", validation.Error(), validation.Field)
	case errors.Is(err, controlplane.ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", `Bearer realm="gwai"`)
		h.writeAPIError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "Incorrect API key provided", "")
	case errors.Is(err, controlplane.ErrForbidden):
		h.writeAPIError(w, http.StatusForbidden, "permission_error", "model_not_allowed", "The API key is not allowed to use the requested model", "model")
	case errors.Is(err, controlplane.ErrNotFound):
		h.writeAPIError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", "The requested model does not exist", "model")
	case errors.As(err, &daprError) && daprError.StatusCode == http.StatusTooManyRequests:
		h.writeAPIError(w, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", "The upstream provider rate limit was exceeded", "")
	default:
		h.logger.Error("Responses request failed", "request_id", platform.RequestID(r.Context()), "error", err)
		h.writeAPIError(w, http.StatusBadGateway, "api_error", "upstream_error", "The gateway could not complete the upstream request", "")
	}
}

func (h *GatewayHandler) createResponse(w http.ResponseWriter, r *http.Request) {
	token, ok := platform.BearerToken(r.Header.Get("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="gwai"`)
		h.writeAPIError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "An API key must be supplied as a Bearer token", "")
		return
	}
	var request CreateRequest
	if err := platform.DecodeJSON(r, &request, h.maxBody, true); err != nil {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", err.Error(), "")
		return
	}
	requestID := platform.RequestID(r.Context())
	internalResponse, err := h.dispatcher.Generate(r.Context(), token, request.Model, requestID, func(route controlplane.Route, id string) (result ir.Request, translateErr error) {
		return ToIR(request, route, id)
	})
	if err != nil {
		h.writeRuntimeError(w, r, err)
		return
	}
	responseID, err := platform.NewID("resp")
	if err != nil {
		h.writeRuntimeError(w, r, err)
		return
	}
	parallel := true
	if request.ParallelToolCalls != nil {
		parallel = *request.ParallelToolCalls
	}
	response, err := FromIR(internalResponse, request.Model, responseID, h.now(), parallel)
	if err != nil {
		h.writeRuntimeError(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, response)
}
