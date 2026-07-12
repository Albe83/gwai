package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/platform"
)

func run() error {
	logger := platform.NewLogger("control-plane")
	adminToken, err := platform.RequiredEnv("GWAI_ADMIN_TOKEN")
	if err != nil {
		return err
	}
	maxBody, err := platform.EnvInt64("GWAI_MAX_BODY_BYTES", 1<<20)
	if err != nil {
		return err
	}
	port := platform.Env("PORT", "8080")
	daprPort := platform.Env("DAPR_HTTP_PORT", "3500")
	daprClient := daprhttp.New("http://127.0.0.1:"+daprPort, os.Getenv("DAPR_API_TOKEN"), &http.Client{Timeout: 15 * time.Second})
	users := controlplane.NewUserRepository(daprhttp.NewStateStore(
		daprClient, platform.Env("GWAI_CONTROL_STATE_STORE", "gwai-control-state"),
	))
	providerStore := daprhttp.NewStateStore(
		daprClient, platform.Env("GWAI_PROVIDER_STATE_STORE", "gwai-provider-state"),
	)
	providers := controlplane.NewProviderRepository(providerStore)
	models := controlplane.NewModelRepository(providerStore)
	subjects := controlplane.NewRemoteSubjectRegistry(
		daprClient, platform.Env("GWAI_VIRTUAL_KEY_CONTROL_APP_ID", "gwai-virtual-key-control-plane"),
	)
	service := controlplane.NewResourceService(users, providers, models, subjects, subjects)
	handler := controlplane.NewResourceHTTPHandler(service, adminToken, maxBody, logger)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return platform.Serve(context.Background(), logger, server)
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
