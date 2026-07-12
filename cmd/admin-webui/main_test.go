package main

import (
	"testing"
	"time"
)

func TestLoadRuntimeConfigUsesHelmEnvironmentContract(t *testing.T) {
	t.Setenv("GWAI_ADMIN_TOKEN", "admin")
	t.Setenv("GWAI_RESOURCE_CONTROL_APP_ID", "resource-app")
	t.Setenv("GWAI_VIRTUAL_KEY_CONTROL_APP_ID", "key-app")
	t.Setenv("GWAI_ADMIN_UI_SESSION_TTL", "1800s")
	t.Setenv("GWAI_ADMIN_UI_SECURE_COOKIES", "true")
	t.Setenv("GWAI_MAX_BODY_BYTES", "65536")
	t.Setenv("DAPR_HTTP_PORT", "3600")
	t.Setenv("DAPR_API_TOKEN", "dapr-token")
	t.Setenv("PORT", "9090")

	config, err := loadRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.resourceAppID != "resource-app" || config.virtualKeyAppID != "key-app" {
		t.Fatalf("app IDs not loaded: %+v", config)
	}
	if config.sessionTTL != 30*time.Minute || !config.cookieSecure || config.maxBodyBytes != 65536 {
		t.Fatalf("admin UI settings not loaded: %+v", config)
	}
	if config.daprPort != "3600" || config.daprAPIToken != "dapr-token" || config.port != "9090" {
		t.Fatalf("runtime settings not loaded: %+v", config)
	}
}

func TestLoadRuntimeConfigRejectsInvalidValues(t *testing.T) {
	t.Setenv("GWAI_ADMIN_TOKEN", "admin")
	t.Setenv("GWAI_ADMIN_UI_SECURE_COOKIES", "sometimes")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("invalid secure-cookie boolean was accepted")
	}
	t.Setenv("GWAI_ADMIN_UI_SECURE_COOKIES", "true")
	t.Setenv("GWAI_ADMIN_UI_SESSION_TTL", "not-a-duration")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("invalid session TTL was accepted")
	}
	t.Setenv("GWAI_ADMIN_UI_SESSION_TTL", "30m")
	t.Setenv("GWAI_REQUEST_TIMEOUT", "0s")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("non-positive request timeout was accepted")
	}
}
