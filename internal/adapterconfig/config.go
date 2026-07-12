// Package adapterconfig loads the cluster-owned identity and upstream
// connection settings shared by provider adapter processes.
package adapterconfig

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/Albe83/gwai/internal/daprhttp"
)

const (
	EnvAppID           = "GWAI_ADAPTER_APP_ID"
	EnvBaseURL         = "GWAI_PROVIDER_BASE_URL"
	EnvAPIVersion      = "GWAI_PROVIDER_API_VERSION"
	EnvSecretStore     = "GWAI_PROVIDER_SECRET_STORE"
	EnvSecretName      = "GWAI_PROVIDER_SECRET_NAME"
	EnvSecretKey       = "GWAI_PROVIDER_SECRET_KEY"
	EnvSecretNamespace = "GWAI_PROVIDER_SECRET_NAMESPACE"
)

var (
	appIDPattern      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)
	apiVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Config is the immutable adapter identity and provider connection selected by
// the cluster administrator. SecretRef identifies credential material but never
// contains the credential itself.
type Config struct {
	AppID      string
	BaseURL    string
	APIVersion string
	SecretRef  daprhttp.SecretRef
}

func required(key string) (string, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}
	return value, nil
}

func rejectControls(key, value string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("environment variable %s contains unsupported control characters", key)
	}
	return nil
}

// Load reads and validates the adapter configuration from the process
// environment. It returns an error before the adapter starts serving traffic if
// any required connection setting is absent or malformed.
func Load() (Config, error) {
	appID, err := required(EnvAppID)
	if err != nil {
		return Config{}, err
	}
	if !appIDPattern.MatchString(appID) {
		return Config{}, fmt.Errorf("environment variable %s must be a valid lowercase Dapr app ID", EnvAppID)
	}

	baseURL, err := required(EnvBaseURL)
	if err != nil {
		return Config{}, err
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Config{}, fmt.Errorf("environment variable %s must be an absolute HTTP(S) URL", EnvBaseURL)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Config{}, fmt.Errorf("environment variable %s must not contain credentials, a query, or a fragment", EnvBaseURL)
	}
	baseURL = strings.TrimRight(baseURL, "/")

	apiVersion, err := required(EnvAPIVersion)
	if err != nil {
		return Config{}, err
	}
	if !apiVersionPattern.MatchString(apiVersion) {
		return Config{}, fmt.Errorf("environment variable %s must be a valid API version token", EnvAPIVersion)
	}

	secretStore, err := required(EnvSecretStore)
	if err != nil {
		return Config{}, err
	}
	secretName, err := required(EnvSecretName)
	if err != nil {
		return Config{}, err
	}
	secretKey, err := required(EnvSecretKey)
	if err != nil {
		return Config{}, err
	}
	secretNamespace := strings.TrimSpace(os.Getenv(EnvSecretNamespace))
	for _, field := range []struct {
		key   string
		value string
	}{
		{key: EnvSecretStore, value: secretStore},
		{key: EnvSecretName, value: secretName},
		{key: EnvSecretKey, value: secretKey},
		{key: EnvSecretNamespace, value: secretNamespace},
	} {
		if err := rejectControls(field.key, field.value); err != nil {
			return Config{}, err
		}
	}

	return Config{
		AppID: appID, BaseURL: baseURL, APIVersion: apiVersion,
		SecretRef: daprhttp.SecretRef{
			Store: secretStore, Name: secretName, Key: secretKey, Namespace: secretNamespace,
		},
	}, nil
}
