package gemini

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/dataplane"
	"github.com/Albe83/gwai/internal/ir"
	"github.com/Albe83/gwai/internal/platform"
)

type GatewayConfig struct {
	APIVersion     string
	MaxBody        int64
	RequestTimeout time.Duration
}

type GatewayHTTPHandler struct {
	dispatcher *dataplane.Dispatcher
	apiVersion string
	maxBody    int64
	logger     *slog.Logger
}

func NewGatewayHTTPHandler(runtime dataplane.Runtime, invoker dataplane.Invoker, config GatewayConfig, logger *slog.Logger) http.Handler {
	apiVersion := strings.Trim(config.APIVersion, "/")
	if apiVersion == "" {
		apiVersion = "v1beta"
	}
	handler := &GatewayHTTPHandler{
		dispatcher: dataplane.NewDispatcher(runtime, invoker, config.RequestTimeout),
		apiVersion: apiVersion,
		maxBody:    config.MaxBody,
		logger:     logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", handler.health)
	mux.HandleFunc("GET /readyz", handler.health)
	mux.HandleFunc("POST /{apiVersion}/models/{operation...}", handler.generate)
	mux.HandleFunc("/", handler.notFound)
	return platform.HTTPMiddleware(logger, mux)
}

func (h *GatewayHTTPHandler) health(w http.ResponseWriter, _ *http.Request) {
	platform.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *GatewayHTTPHandler) writeError(w http.ResponseWriter, status int, apiStatus, message string) {
	platform.JSON(w, status, ErrorResponse{Error: APIError{Code: status, Message: message, Status: apiStatus}})
}

func (h *GatewayHTTPHandler) notFound(w http.ResponseWriter, _ *http.Request) {
	h.writeError(w, http.StatusNotFound, "NOT_FOUND", "the requested Gemini API endpoint does not exist")
}

func (h *GatewayHTTPHandler) writeRuntimeError(w http.ResponseWriter, r *http.Request, err error) {
	var translation *TranslationError
	var validation *controlplane.ValidationError
	var daprError *daprhttp.HTTPError
	switch {
	case errors.As(err, &translation):
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", translation.Message)
	case errors.As(err, &validation):
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", validation.Error())
	case errors.Is(err, controlplane.ErrUnauthorized):
		h.writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "API key not valid")
	case errors.Is(err, controlplane.ErrForbidden):
		h.writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "the API key is not allowed to use the requested model")
	case errors.Is(err, controlplane.ErrNotFound):
		h.writeError(w, http.StatusNotFound, "NOT_FOUND", "the requested model does not exist")
	case errors.As(err, &daprError) && daprError.StatusCode == http.StatusTooManyRequests:
		h.writeError(w, http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "the upstream provider rate limit was exceeded")
	case errors.Is(err, context.DeadlineExceeded):
		h.writeError(w, http.StatusGatewayTimeout, "DEADLINE_EXCEEDED", "the request exceeded its deadline")
	default:
		h.logger.Error("Gemini generation failed", "request_id", platform.RequestID(r.Context()), "error", err)
		h.writeError(w, http.StatusBadGateway, "UNAVAILABLE", "the gateway could not complete the upstream request")
	}
}

func (h *GatewayHTTPHandler) generate(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("apiVersion") != h.apiVersion {
		h.notFound(w, r)
		return
	}
	operation := r.PathValue("operation")
	if strings.HasSuffix(operation, ":streamGenerateContent") {
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "streamGenerateContent is not supported; use generateContent")
		return
	}
	model, ok := strings.CutSuffix(operation, ":generateContent")
	if !ok || strings.TrimSpace(model) == "" {
		h.notFound(w, r)
		return
	}
	token := strings.TrimSpace(r.Header.Get("x-goog-api-key"))
	if token == "" {
		h.writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "an API key must be supplied in the x-goog-api-key header")
		return
	}
	var request GenerateContentRequest
	if err := platform.DecodeJSON(r, &request, h.maxBody, true); err != nil {
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	requestID := platform.RequestID(r.Context())
	response, err := h.dispatcher.Generate(r.Context(), token, model, requestID, func(route controlplane.Route, id string) (irRequest ir.Request, err error) {
		return ToIRRequest(request, model, route, id)
	})
	if err != nil {
		h.writeRuntimeError(w, r, err)
		return
	}
	providerResponse, err := FromIRResponse(response)
	if err != nil {
		h.writeRuntimeError(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, providerResponse)
}
