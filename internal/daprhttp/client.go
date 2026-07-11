// Package daprhttp implements the Dapr HTTP building-block APIs used by gwai.
// Keeping this adapter small avoids coupling domain code to a Dapr SDK.
package daprhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Dapr returned HTTP %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	baseURL  string
	apiToken string
	http     *http.Client
}

func New(baseURL, apiToken string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiToken: apiToken,
		http:     httpClient,
	}
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, body)
	if err != nil {
		return nil, err
	}
	if c.apiToken != "" {
		request.Header.Set("dapr-api-token", c.apiToken)
	}
	return request, nil
}

func (c *Client) Invoke(ctx context.Context, appID, method string, body io.Reader, contentType string) (*http.Response, error) {
	endpoint := "/v1.0/invoke/" + url.PathEscape(appID) + "/method/" + strings.TrimLeft(method, "/")
	request, err := c.newRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create Dapr invocation request: %w", err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(request)
}

func (c *Client) InvokeJSON(ctx context.Context, appID, method string, input, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode Dapr invocation request: %w", err)
	}
	response, err := c.Invoke(ctx, appID, method, bytes.NewReader(payload), "application/json")
	if err != nil {
		return fmt.Errorf("invoke Dapr app %q: %w", appID, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode response from Dapr app %q: %w", appID, err)
	}
	return nil
}
