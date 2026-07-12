package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/openairesponses"
	"github.com/Albe83/gwai/internal/platform"
)

func run() error {
	logger := platform.NewLogger("openai-responses-adapter")
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
	maxOutputTokens, err := platform.EnvInt64("GWAI_MAX_OUTPUT_TOKENS", 0)
	if err != nil {
		return err
	}
	if maxOutputTokens < 0 {
		return fmt.Errorf("GWAI_MAX_OUTPUT_TOKENS must not be negative")
	}
	port := platform.Env("PORT", "8080")
	daprPort := platform.Env("DAPR_HTTP_PORT", "3500")
	daprClient := daprhttp.New("http://127.0.0.1:"+daprPort, os.Getenv("DAPR_API_TOKEN"), &http.Client{})
	providers := controlplane.NewProviderRepository(daprhttp.NewStateStore(daprClient, platform.Env("GWAI_PROVIDER_STATE_STORE", "gwai-provider-state")))
	runtime := controlplane.NewProviderRuntime(providers)
	secretStore := daprhttp.NewSecretStore(daprClient)
	handler := openairesponses.NewAdapterHTTPHandler(runtime, secretStore, openairesponses.NewUpstreamClient(requestTimeout), openairesponses.AdapterConfig{
		ProviderSlug: providerSlug, AppID: appID, MaxBody: maxBody, AppToken: os.Getenv("APP_API_TOKEN"), MaxOutputTokens: int(maxOutputTokens),
	}, logger)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       90 * time.Second,
	}
	return platform.Serve(context.Background(), logger, server)
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
