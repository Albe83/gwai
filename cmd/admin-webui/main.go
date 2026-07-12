package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Albe83/gwai/internal/adminui"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/platform"
)

func envBool(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", key, err)
	}
	return value, nil
}

type runtimeConfig struct {
	adminToken      string
	resourceAppID   string
	virtualKeyAppID string
	sessionTTL      time.Duration
	requestTimeout  time.Duration
	maxBodyBytes    int64
	cookieSecure    bool
	cookieName      string
	port            string
	daprPort        string
	daprAPIToken    string
}

func loadRuntimeConfig() (runtimeConfig, error) {
	var config runtimeConfig
	var err error
	config.adminToken, err = platform.RequiredEnv("GWAI_ADMIN_TOKEN")
	if err != nil {
		return runtimeConfig{}, err
	}
	config.sessionTTL, err = platform.EnvDuration("GWAI_ADMIN_UI_SESSION_TTL", 30*time.Minute)
	if err != nil {
		return runtimeConfig{}, err
	}
	config.requestTimeout, err = platform.EnvDuration("GWAI_REQUEST_TIMEOUT", 20*time.Second)
	if err != nil {
		return runtimeConfig{}, err
	}
	if config.requestTimeout <= 0 {
		return runtimeConfig{}, fmt.Errorf("GWAI_REQUEST_TIMEOUT must be positive")
	}
	config.maxBodyBytes, err = platform.EnvInt64("GWAI_MAX_BODY_BYTES", 128<<10)
	if err != nil {
		return runtimeConfig{}, err
	}
	config.cookieSecure, err = envBool("GWAI_ADMIN_UI_SECURE_COOKIES", false)
	if err != nil {
		return runtimeConfig{}, err
	}
	config.resourceAppID = platform.Env("GWAI_RESOURCE_CONTROL_APP_ID", "gwai-control-plane")
	config.virtualKeyAppID = platform.Env("GWAI_VIRTUAL_KEY_CONTROL_APP_ID", "gwai-virtual-key-control-plane")
	config.cookieName = platform.Env("GWAI_SESSION_COOKIE_NAME", "gwai_admin_session")
	config.port = platform.Env("PORT", "8080")
	config.daprPort = platform.Env("DAPR_HTTP_PORT", "3500")
	config.daprAPIToken = os.Getenv("DAPR_API_TOKEN")
	return config, nil
}

func run() error {
	logger := platform.NewLogger("admin-webui")
	config, err := loadRuntimeConfig()
	if err != nil {
		return err
	}

	daprClient := daprhttp.New(
		"http://127.0.0.1:"+config.daprPort, config.daprAPIToken,
		&http.Client{Timeout: config.requestTimeout},
	)
	api, err := adminui.NewDaprAPI(
		daprClient,
		config.resourceAppID, config.virtualKeyAppID, config.adminToken,
	)
	if err != nil {
		return err
	}
	handler, err := adminui.NewHandler(api, adminui.Config{
		AdminToken: config.adminToken, SessionTTL: config.sessionTTL, RequestTimeout: config.requestTimeout,
		CookieName:   config.cookieName,
		CookieSecure: config.cookieSecure, MaxFormBytes: config.maxBodyBytes,
	}, logger)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              ":" + config.port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      config.requestTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	// The grace interval exceeds the handler deadline so a lifecycle command,
	// especially one returning a one-time key secret, can finish during rollout.
	return platform.ServeWithShutdownTimeout(context.Background(), logger, server, config.requestTimeout+5*time.Second)
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
