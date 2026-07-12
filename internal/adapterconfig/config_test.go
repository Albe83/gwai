package adapterconfig

import (
	"strings"
	"testing"
)

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv(EnvAppID, "gwai-openai.primary")
	t.Setenv(EnvBaseURL, " https://provider.example.test/api/ ")
	t.Setenv(EnvAPIVersion, "v1")
	t.Setenv(EnvSecretStore, "kubernetes")
	t.Setenv(EnvSecretName, "provider-credential")
	t.Setenv(EnvSecretKey, "api-key")
	t.Setenv(EnvSecretNamespace, "gwai")
}

func TestLoadNormalizesClusterOwnedConfiguration(t *testing.T) {
	setValidEnvironment(t)

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.AppID != "gwai-openai.primary" || config.BaseURL != "https://provider.example.test/api" || config.APIVersion != "v1" {
		t.Fatalf("unexpected adapter configuration: %#v", config)
	}
	if config.SecretRef.Store != "kubernetes" || config.SecretRef.Name != "provider-credential" ||
		config.SecretRef.Key != "api-key" || config.SecretRef.Namespace != "gwai" {
		t.Fatalf("unexpected secret reference: %#v", config.SecretRef)
	}
}

func TestLoadAllowsOptionalSecretNamespace(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv(EnvSecretNamespace, "")

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.SecretRef.Key != "api-key" || config.SecretRef.Namespace != "" {
		t.Fatalf("secret fields were not preserved: %#v", config.SecretRef)
	}
}

func TestLoadRejectsMissingRequiredEnvironment(t *testing.T) {
	required := []string{EnvAppID, EnvBaseURL, EnvAPIVersion, EnvSecretStore, EnvSecretName, EnvSecretKey}
	for _, missing := range required {
		t.Run(missing, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(missing, " \t ")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("expected an error naming %s, got %v", missing, err)
			}
		})
	}
}

func TestLoadRejectsInvalidIdentityAndEndpoint(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "app id", key: EnvAppID, value: "Invalid_App"},
		{name: "relative URL", key: EnvBaseURL, value: "/v1"},
		{name: "credential URL", key: EnvBaseURL, value: "https://user:pass@example.test"},
		{name: "query URL", key: EnvBaseURL, value: "https://example.test?tenant=a"},
		{name: "API version", key: EnvAPIVersion, value: "v1/beta"},
		{name: "secret control character", key: EnvSecretName, value: "secret\nother"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.key, test.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("expected an error naming %s, got %v", test.key, err)
			}
		})
	}
}
