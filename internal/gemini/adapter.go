package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/ir"
	"github.com/Albe83/gwai/internal/platform"
)

type ProviderResolver interface {
	ResolveProviderBySlug(context.Context, string) (controlplane.Provider, error)
}

type SecretResolver interface {
	Get(context.Context, daprhttp.SecretRef) (string, error)
}

type AdapterConfig struct {
	ProviderSlug           string
	AppID                  string
	MaxBody                int64
	AppToken               string
	DefaultMaxOutputTokens int
	MaxOutputTokens        int
}

type AdapterHTTPHandler struct {
	providers ProviderResolver
	secrets   SecretResolver
	upstream  *http.Client
	config    AdapterConfig
	logger    *slog.Logger
}

func NewAdapterHTTPHandler(providers ProviderResolver, secrets SecretResolver, upstream *http.Client, config AdapterConfig, logger *slog.Logger) http.Handler {
	if upstream == nil {
		upstream = http.DefaultClient
	}
	handler := &AdapterHTTPHandler{providers: providers, secrets: secrets, upstream: upstream, config: config, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", handler.health)
	mux.HandleFunc("GET /readyz", handler.health)
	mux.Handle("POST /v1/generate", handler.internal(http.HandlerFunc(handler.generate)))
	return platform.HTTPMiddleware(logger, mux)
}

func (h *AdapterHTTPHandler) internal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.config.AppToken != "" && !platform.SecureEqual(r.Header.Get("dapr-api-token"), h.config.AppToken) {
			platform.WriteProblem(w, r, http.StatusUnauthorized, "Unauthorized", "the endpoint is only available through the authenticated Dapr sidecar")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *AdapterHTTPHandler) health(w http.ResponseWriter, _ *http.Request) {
	platform.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *AdapterHTTPHandler) fail(w http.ResponseWriter, r *http.Request, status int, title, detail string, err error) {
	if err != nil {
		h.logger.Error("Gemini adapter request failed", "request_id", platform.RequestID(r.Context()), "error", err)
	}
	platform.WriteProblem(w, r, status, title, detail)
}

func providerGenerateContentURL(provider controlplane.Provider, upstreamModel string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(provider.BaseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Gemini provider base URL")
	}
	apiVersion := strings.Trim(provider.APIVersion, "/")
	if apiVersion == "" {
		return "", fmt.Errorf("Gemini provider api_version is required")
	}
	model := strings.TrimPrefix(strings.TrimSpace(upstreamModel), "models/")
	if model == "" {
		return "", fmt.Errorf("Gemini upstream model is required")
	}
	return strings.TrimRight(parsed.String(), "/") + "/" + url.PathEscape(apiVersion) + "/models/" + url.PathEscape(model) + ":generateContent", nil
}

func (h *AdapterHTTPHandler) generate(w http.ResponseWriter, r *http.Request) {
	var internalRequest ir.Request
	if err := platform.DecodeJSON(r, &internalRequest, h.config.MaxBody, true); err != nil {
		h.fail(w, r, http.StatusBadRequest, "Invalid IR request", err.Error(), nil)
		return
	}
	if err := internalRequest.Validate(); err != nil {
		h.fail(w, r, http.StatusBadRequest, "Invalid IR request", err.Error(), nil)
		return
	}
	provider, err := h.providers.ResolveProviderBySlug(r.Context(), h.config.ProviderSlug)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, controlplane.ErrNotFound) || errors.Is(err, controlplane.ErrForbidden) {
			status = http.StatusUnprocessableEntity
		}
		h.fail(w, r, status, "Provider resolution failed", "the configured provider is unavailable", err)
		return
	}
	if provider.Kind != controlplane.ProviderKindGemini {
		h.fail(w, r, http.StatusUnprocessableEntity, "Invalid provider", "this adapter only accepts Gemini providers", nil)
		return
	}
	if provider.ID != internalRequest.Route.ProviderID || provider.AdapterAppID != h.config.AppID {
		h.fail(w, r, http.StatusUnprocessableEntity, "Invalid route", "the request is not addressed to this provider adapter", nil)
		return
	}
	apiKey, err := h.secrets.Get(r.Context(), provider.SecretRef)
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, "Provider credential unavailable", "the provider credential could not be loaded", err)
		return
	}
	if strings.TrimSpace(apiKey) == "" {
		h.fail(w, r, http.StatusBadGateway, "Provider credential unavailable", "the provider credential is empty", nil)
		return
	}
	providerRequest, err := ToProviderRequest(internalRequest, h.config.DefaultMaxOutputTokens, h.config.MaxOutputTokens)
	if err != nil {
		h.fail(w, r, http.StatusBadRequest, "Unsupported IR request", err.Error(), nil)
		return
	}
	payload, err := json.Marshal(providerRequest)
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, "Encoding failed", "the provider request could not be encoded", err)
		return
	}
	endpoint, err := providerGenerateContentURL(provider, internalRequest.Route.UpstreamModel)
	if err != nil {
		h.fail(w, r, http.StatusUnprocessableEntity, "Invalid provider", err.Error(), nil)
		return
	}
	upstreamRequest, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, "Request construction failed", "the provider request could not be created", err)
		return
	}
	upstreamRequest.Header.Set("Content-Type", "application/json")
	upstreamRequest.Header.Set("Accept", "application/json")
	upstreamRequest.Header.Set("x-goog-api-key", apiKey)
	upstreamRequest.Header.Set("User-Agent", "gwai-gemini-adapter/0.1")
	if requestID := platform.RequestID(r.Context()); requestID != "" {
		upstreamRequest.Header.Set("X-Request-ID", requestID)
	}

	upstreamResponse, err := h.upstream.Do(upstreamRequest)
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, "Provider request failed", "the Gemini API could not be reached", err)
		return
	}
	defer upstreamResponse.Body.Close()
	body, err := io.ReadAll(io.LimitReader(upstreamResponse.Body, 20<<20))
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, "Provider response failed", "the Gemini response could not be read", err)
		return
	}
	if upstreamResponse.StatusCode < 200 || upstreamResponse.StatusCode >= 300 {
		var providerError ErrorResponse
		_ = json.Unmarshal(body, &providerError)
		status := http.StatusBadGateway
		if upstreamResponse.StatusCode == http.StatusTooManyRequests {
			status = http.StatusTooManyRequests
		}
		detail := fmt.Sprintf("Gemini returned HTTP %d", upstreamResponse.StatusCode)
		logError := fmt.Errorf("%s (status=%s, message=%s)", detail, providerError.Error.Status, providerError.Error.Message)
		h.fail(w, r, status, "Provider rejected request", detail, logError)
		return
	}
	var providerResponse GenerateContentResponse
	if err := json.Unmarshal(body, &providerResponse); err != nil {
		h.fail(w, r, http.StatusBadGateway, "Invalid provider response", "Gemini returned malformed JSON", err)
		return
	}
	internalResponse, err := ToIRResponse(providerResponse, internalRequest)
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, "Unsupported provider response", "Gemini returned a response that cannot be represented", err)
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
