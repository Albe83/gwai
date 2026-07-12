package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Albe83/gwai/internal/adapterconfig"
	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/ir"
	"github.com/Albe83/gwai/internal/platform"
)

type ProviderResolver interface {
	ResolveProviderByAdapterAppID(context.Context, string) (controlplane.Provider, error)
}

type SecretResolver interface {
	Get(context.Context, daprhttp.SecretRef) (string, error)
}

type HTTPHandler struct {
	providers ProviderResolver
	secrets   SecretResolver
	upstream  *http.Client
	maxBody   int64
	appToken  string
	config    Config
	logger    *slog.Logger
}

type Config struct {
	Runtime                adapterconfig.Config
	MaxBody                int64
	AppToken               string
	DefaultMaxOutputTokens int
	MaxOutputTokens        int
}

func NewHTTPHandler(providers ProviderResolver, secrets SecretResolver, upstream *http.Client, config Config, logger *slog.Logger) http.Handler {
	if upstream == nil {
		upstream = http.DefaultClient
	}
	handler := &HTTPHandler{
		providers: providers, secrets: secrets, upstream: upstream,
		maxBody: config.MaxBody, appToken: config.AppToken, config: config, logger: logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", handler.health)
	mux.HandleFunc("GET /readyz", handler.health)
	mux.Handle("POST /v1/generate", handler.internal(http.HandlerFunc(handler.generate)))
	return platform.HTTPMiddleware(logger, mux)
}

func (h *HTTPHandler) internal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.appToken != "" && !platform.SecureEqual(r.Header.Get("dapr-api-token"), h.appToken) {
			platform.WriteProblem(w, r, http.StatusUnauthorized, "Unauthorized", "the endpoint is only available through the authenticated Dapr sidecar")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *HTTPHandler) health(w http.ResponseWriter, _ *http.Request) {
	platform.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *HTTPHandler) fail(w http.ResponseWriter, r *http.Request, status int, title, detail string, err error) {
	if err != nil {
		h.logger.Error("anthropic adapter request failed", "request_id", platform.RequestID(r.Context()), "error", err)
	}
	platform.WriteProblem(w, r, status, title, detail)
}

func (h *HTTPHandler) generate(w http.ResponseWriter, r *http.Request) {
	var internalRequest ir.Request
	if err := platform.DecodeJSON(r, &internalRequest, h.maxBody, true); err != nil {
		h.fail(w, r, http.StatusBadRequest, "Invalid IR request", err.Error(), nil)
		return
	}
	if err := internalRequest.Validate(); err != nil {
		h.fail(w, r, http.StatusBadRequest, "Invalid IR request", err.Error(), nil)
		return
	}
	provider, err := h.providers.ResolveProviderByAdapterAppID(r.Context(), h.config.Runtime.AppID)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, controlplane.ErrNotFound) || errors.Is(err, controlplane.ErrForbidden) {
			status = http.StatusUnprocessableEntity
		}
		h.fail(w, r, status, "Provider resolution failed", "the configured provider is unavailable", err)
		return
	}
	if provider.Kind != controlplane.ProviderKindAnthropic {
		h.fail(w, r, http.StatusUnprocessableEntity, "Invalid provider", "this adapter only accepts Anthropic providers", nil)
		return
	}
	if provider.ID != internalRequest.Route.ProviderID || provider.AdapterAppID != h.config.Runtime.AppID {
		h.fail(w, r, http.StatusUnprocessableEntity, "Invalid route", "the request is not addressed to this provider adapter", nil)
		return
	}
	apiKey, err := h.secrets.Get(r.Context(), h.config.Runtime.SecretRef)
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, "Provider credential unavailable", "the provider credential could not be loaded", err)
		return
	}
	if strings.TrimSpace(apiKey) == "" {
		h.fail(w, r, http.StatusBadGateway, "Provider credential unavailable", "the provider credential is empty", nil)
		return
	}
	providerRequest, err := ToMessageRequest(internalRequest, h.config.DefaultMaxOutputTokens, h.config.MaxOutputTokens)
	if err != nil {
		h.fail(w, r, http.StatusBadRequest, "Unsupported IR request", err.Error(), nil)
		return
	}
	payload, err := json.Marshal(providerRequest)
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, "Encoding failed", "the provider request could not be encoded", err)
		return
	}
	upstreamRequest, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.config.Runtime.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, "Request construction failed", "the provider request could not be created", err)
		return
	}
	upstreamRequest.Header.Set("Content-Type", "application/json")
	upstreamRequest.Header.Set("Accept", "application/json")
	upstreamRequest.Header.Set("x-api-key", apiKey)
	upstreamRequest.Header.Set("anthropic-version", h.config.Runtime.APIVersion)
	upstreamRequest.Header.Set("User-Agent", "gwai-anthropic-adapter/0.2")
	if requestID := platform.RequestID(r.Context()); requestID != "" {
		upstreamRequest.Header.Set("X-Request-ID", requestID)
	}

	upstreamResponse, err := h.upstream.Do(upstreamRequest)
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, "Provider request failed", "the Anthropic API could not be reached", err)
		return
	}
	defer upstreamResponse.Body.Close()
	body, err := io.ReadAll(io.LimitReader(upstreamResponse.Body, 20<<20))
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, "Provider response failed", "the Anthropic response could not be read", err)
		return
	}
	if upstreamResponse.StatusCode < 200 || upstreamResponse.StatusCode >= 300 {
		var providerError ErrorResponse
		_ = json.Unmarshal(body, &providerError)
		status := http.StatusBadGateway
		if upstreamResponse.StatusCode == http.StatusTooManyRequests {
			status = http.StatusTooManyRequests
		}
		detail := fmt.Sprintf("Anthropic returned HTTP %d", upstreamResponse.StatusCode)
		logError := fmt.Errorf("%s (type=%s, request_id=%s)", detail, providerError.Error.Type, providerError.RequestID)
		h.fail(w, r, status, "Provider rejected request", detail, logError)
		return
	}
	var providerResponse MessageResponse
	if err := json.Unmarshal(body, &providerResponse); err != nil {
		h.fail(w, r, http.StatusBadGateway, "Invalid provider response", "Anthropic returned malformed JSON", err)
		return
	}
	internalResponse, err := ToIRResponse(providerResponse, internalRequest)
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, "Unsupported provider response", "Anthropic returned a response that cannot be represented", err)
		return
	}
	platform.JSON(w, http.StatusOK, internalResponse)
}

func NewUpstreamClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: transport, Timeout: timeout}
}
