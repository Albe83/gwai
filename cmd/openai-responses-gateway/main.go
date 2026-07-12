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
	logger := platform.NewLogger("openai-responses-gateway")
	maxBody, err := platform.EnvInt64("GWAI_MAX_BODY_BYTES", 10<<20)
	if err != nil {
		return err
	}
	requestTimeout, err := platform.EnvDuration("GWAI_REQUEST_TIMEOUT", 5*time.Minute)
	if err != nil {
		return err
	}
	port := platform.Env("PORT", "8080")
	daprPort := platform.Env("DAPR_HTTP_PORT", "3500")
	daprClient := daprhttp.New("http://127.0.0.1:"+daprPort, os.Getenv("DAPR_API_TOKEN"), &http.Client{})
	keys := controlplane.NewVirtualKeyRepository(daprhttp.NewStateStore(daprClient, platform.Env("GWAI_VIRTUAL_KEY_STATE_STORE", "gwai-virtual-key-state")))
	providerStore := daprhttp.NewStateStore(daprClient, platform.Env("GWAI_PROVIDER_STATE_STORE", "gwai-provider-state"))
	models := controlplane.NewModelRepository(providerStore)
	providers := controlplane.NewProviderRepository(providerStore)
	runtime := controlplane.NewGatewayRuntime(keys, models, providers)
	handler := openairesponses.NewGatewayHTTPHandler(runtime, daprClient, maxBody, requestTimeout, logger)

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
