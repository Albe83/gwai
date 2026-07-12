package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/gemini"
	"github.com/Albe83/gwai/internal/platform"
)

func run() error {
	logger := platform.NewLogger("gemini-adapter")
	maxBody, err := platform.EnvInt64("GWAI_MAX_BODY_BYTES", 10<<20)
	if err != nil {
		return err
	}
	requestTimeout, err := platform.EnvDuration("GWAI_PROVIDER_TIMEOUT", 5*time.Minute)
	if err != nil {
		return err
	}
	providerSlug, err := platform.RequiredEnv("GWAI_PROVIDER_SLUG")
	if err != nil {
		return err
	}
	appID, err := platform.RequiredEnv("GWAI_ADAPTER_APP_ID")
	if err != nil {
		return err
	}
	defaultMaxOutputTokens, err := platform.EnvInt64("GWAI_DEFAULT_MAX_OUTPUT_TOKENS", 4096)
	if err != nil {
		return err
	}
	if defaultMaxOutputTokens < 0 {
		return fmt.Errorf("GWAI_DEFAULT_MAX_OUTPUT_TOKENS must not be negative")
	}
	maxOutputTokens, err := platform.EnvInt64("GWAI_MAX_OUTPUT_TOKENS", 0)
	if err != nil {
		return err
	}
	if maxOutputTokens < 0 || (maxOutputTokens > 0 && maxOutputTokens < defaultMaxOutputTokens) {
		return fmt.Errorf("GWAI_MAX_OUTPUT_TOKENS must be zero or at least GWAI_DEFAULT_MAX_OUTPUT_TOKENS")
	}
	port := platform.Env("PORT", "8080")
	daprPort := platform.Env("DAPR_HTTP_PORT", "3500")
	daprClient := daprhttp.New("http://127.0.0.1:"+daprPort, os.Getenv("DAPR_API_TOKEN"), &http.Client{})
	store := daprhttp.NewStateStore(daprClient, platform.Env("GWAI_STATE_STORE", "gwai-state"))
	runtime := controlplane.NewRuntime(controlplane.NewRepository(store))
	secretStore := daprhttp.NewSecretStore(daprClient)
	handler := gemini.NewAdapterHTTPHandler(runtime, secretStore, gemini.NewUpstreamClient(requestTimeout), gemini.AdapterConfig{
		ProviderSlug: providerSlug, AppID: appID, MaxBody: maxBody, AppToken: os.Getenv("APP_API_TOKEN"),
		DefaultMaxOutputTokens: int(defaultMaxOutputTokens), MaxOutputTokens: int(maxOutputTokens),
	}, logger)
	server := &http.Server{
		Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 90 * time.Second,
	}
	return platform.Serve(context.Background(), logger, server)
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
