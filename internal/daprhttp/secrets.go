package daprhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type SecretRef struct {
	Store     string `json:"store"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	Namespace string `json:"namespace,omitempty"`
}

type SecretStore struct {
	client *Client
}

func NewSecretStore(client *Client) *SecretStore {
	return &SecretStore{client: client}
}

func (s *SecretStore) Get(ctx context.Context, ref SecretRef) (string, error) {
	query := make(url.Values)
	if ref.Namespace != "" {
		query.Set("metadata.namespace", ref.Namespace)
	}
	endpoint := "/v1.0/secrets/" + url.PathEscape(ref.Store) + "/" + url.PathEscape(ref.Name)
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	request, err := s.client.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create Dapr secret request: %w", err)
	}
	response, err := s.client.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("get Dapr secret %q: %w", ref.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return "", &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	var values map[string]string
	if err := json.NewDecoder(response.Body).Decode(&values); err != nil {
		return "", fmt.Errorf("decode Dapr secret %q: %w", ref.Name, err)
	}
	key := ref.Key
	if key == "" {
		key = ref.Name
	}
	value, ok := values[key]
	if !ok || value == "" {
		return "", fmt.Errorf("secret %q does not contain non-empty key %q", ref.Name, key)
	}
	return value, nil
}
