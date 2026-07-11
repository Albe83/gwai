package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Albe83/gwai/internal/anthropic"
	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/platform"
)

func run() error {
	logger := platform.NewLogger("anthropic-adapter")
	maxBody, err := platform.EnvInt64("GWAI_MAX_BODY_BYTES", 10<<20)
	if err != nil {
		return err
	}
	requestTimeout, err := platform.EnvDuration("GWAI_PROVIDER_TIMEOUT", 5*time.Minute)
	if err != nil {
		return err
	}
	port := platform.Env("PORT", "8080")
	daprPort := platform.Env("DAPR_HTTP_PORT", "3500")
	daprClient := daprhttp.New("http://127.0.0.1:"+daprPort, os.Getenv("DAPR_API_TOKEN"), &http.Client{})
	controlPlane := controlplane.NewClient(daprClient, platform.Env("GWAI_CONTROL_PLANE_APP_ID", "gwai-control-plane"))
	secretStore := daprhttp.NewSecretStore(daprClient)
	handler := anthropic.NewHTTPHandler(controlPlane, secretStore, anthropic.NewUpstreamClient(requestTimeout), maxBody, os.Getenv("APP_API_TOKEN"), logger)

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
