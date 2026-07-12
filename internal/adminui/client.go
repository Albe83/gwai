package adminui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/platform"
)

const maxControlPlaneResponse = 4 << 20

type Versioned[T any] struct {
	Value T
	ETag  string
}

// API is the deliberately narrow administrative contract consumed by the UI.
// It is not a generic reverse proxy: every route and target application is
// selected by a concrete method below.
type API interface {
	ListUsers(context.Context) ([]controlplane.User, error)
	GetUser(context.Context, string) (Versioned[controlplane.User], error)
	CreateUser(context.Context, controlplane.UserInput) (controlplane.User, error)
	UpdateUser(context.Context, string, controlplane.UserInput, string) (Versioned[controlplane.User], error)
	DeleteUser(context.Context, string, string) error

	ListProviders(context.Context) ([]controlplane.Provider, error)
	GetProvider(context.Context, string) (Versioned[controlplane.Provider], error)
	CreateProvider(context.Context, controlplane.ProviderInput) (controlplane.Provider, error)
	UpdateProvider(context.Context, string, controlplane.ProviderInput, string) (Versioned[controlplane.Provider], error)
	DeleteProvider(context.Context, string, string) error

	ListModels(context.Context) ([]controlplane.Model, error)
	GetModel(context.Context, string) (Versioned[controlplane.Model], error)
	CreateModel(context.Context, controlplane.ModelInput) (controlplane.Model, error)
	UpdateModel(context.Context, string, controlplane.ModelInput, string) (Versioned[controlplane.Model], error)
	DeleteModel(context.Context, string, string) error

	ListVirtualKeys(context.Context) ([]controlplane.PublicVirtualKey, error)
	GetVirtualKey(context.Context, string) (Versioned[controlplane.PublicVirtualKey], error)
	CreateVirtualKey(context.Context, controlplane.VirtualKeyInput) (controlplane.CreatedVirtualKey, error)
	UpdateVirtualKey(context.Context, string, controlplane.VirtualKeyInput, string) (Versioned[controlplane.PublicVirtualKey], error)
	DeleteVirtualKey(context.Context, string, string) error
}

// APIError retains the safe RFC 9457 response returned by a control plane.
// The UI renders Detail and Instance, but never forwards raw Dapr error bodies.
type APIError struct {
	Status   int
	Title    string
	Detail   string
	Instance string
	// Ambiguous reports that a non-idempotent request may have committed even
	// though its response could not be established safely.
	Ambiguous bool
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.Title != "" {
		return e.Title
	}
	return http.StatusText(e.Status)
}

// DaprAPI invokes the two public administrative APIs through explicit Dapr
// application routes. The admin bearer token remains server-side.
type DaprAPI struct {
	client          *daprhttp.Client
	resourceAppID   string
	virtualKeyAppID string
	adminToken      string
}

// unknownLengthBody deliberately prevents net/http from assigning a
// Content-Length. Dapr treats such invocation bodies as streaming and does not
// apply its built-in retries, which is required for administrative mutations.
type unknownLengthBody struct {
	io.Reader
}

func NewDaprAPI(client *daprhttp.Client, resourceAppID, virtualKeyAppID, adminToken string) (*DaprAPI, error) {
	if client == nil {
		return nil, errors.New("Dapr client is required")
	}
	resourceAppID = strings.TrimSpace(resourceAppID)
	virtualKeyAppID = strings.TrimSpace(virtualKeyAppID)
	if resourceAppID == "" || virtualKeyAppID == "" {
		return nil, errors.New("both control-plane app IDs are required")
	}
	if strings.TrimSpace(adminToken) == "" {
		return nil, errors.New("admin token is required")
	}
	return &DaprAPI{
		client: client, resourceAppID: resourceAppID,
		virtualKeyAppID: virtualKeyAppID, adminToken: adminToken,
	}, nil
}

func (c *DaprAPI) invoke(ctx context.Context, appID, method, path, ifMatch string, input, output any) (string, error) {
	return c.invokeRequest(ctx, appID, method, path, ifMatch, input, output, false)
}

func (c *DaprAPI) invokeWithoutRetries(ctx context.Context, appID, method, path, ifMatch string, input, output any) (string, error) {
	return c.invokeRequest(ctx, appID, method, path, ifMatch, input, output, true)
}

func (c *DaprAPI) invokeRequest(ctx context.Context, appID, method, path, ifMatch string, input, output any, unknownLength bool) (string, error) {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return "", fmt.Errorf("encode control-plane request: %w", err)
		}
		body = bytes.NewReader(payload)
		if unknownLength {
			body = &unknownLengthBody{Reader: body}
		}
	} else if unknownLength {
		// A non-empty whitespace body keeps DELETE unknown-length through the Go
		// transport's empty-body probe. Target handlers intentionally ignore it.
		body = &unknownLengthBody{Reader: strings.NewReader("\n")}
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json, application/problem+json")
	headers.Set("Authorization", "Bearer "+c.adminToken)
	if input != nil {
		headers.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		headers.Set("If-Match", ifMatch)
	}
	response, err := c.client.InvokeMethod(ctx, method, appID, path, body, headers)
	if err != nil {
		return "", &APIError{Status: http.StatusBadGateway, Title: "Control plane unavailable", Detail: "The administrative service could not be reached.", Ambiguous: method != http.MethodGet}
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, maxControlPlaneResponse+1)
	payload, readErr := io.ReadAll(limited)
	if readErr != nil {
		return "", &APIError{Status: http.StatusBadGateway, Title: "Invalid control-plane response", Detail: "The administrative service response could not be read.", Ambiguous: method != http.MethodGet}
	}
	if len(payload) > maxControlPlaneResponse {
		return "", &APIError{Status: http.StatusBadGateway, Title: "Invalid control-plane response", Detail: "The administrative service returned an oversized response.", Ambiguous: method != http.MethodGet}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		problem := platform.Problem{}
		if err := json.Unmarshal(payload, &problem); err != nil || problem.Status == 0 {
			return "", &APIError{
				Status: response.StatusCode, Title: http.StatusText(response.StatusCode),
				Detail:    "The administrative service rejected the request.",
				Instance:  response.Header.Get("X-Request-ID"),
				Ambiguous: method != http.MethodGet && response.StatusCode >= http.StatusInternalServerError,
			}
		}
		return "", &APIError{
			Status: response.StatusCode, Title: problem.Title,
			Detail: problem.Detail, Instance: firstNonEmpty(problem.Instance, response.Header.Get("X-Request-ID")),
			Ambiguous: method != http.MethodGet && response.StatusCode >= http.StatusInternalServerError,
		}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return response.Header.Get("ETag"), nil
	}
	if len(payload) == 0 {
		return "", &APIError{Status: http.StatusBadGateway, Title: "Invalid control-plane response", Detail: "The administrative service returned an empty response.", Ambiguous: method != http.MethodGet}
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return "", &APIError{Status: http.StatusBadGateway, Title: "Invalid control-plane response", Detail: "The administrative service returned invalid JSON.", Ambiguous: method != http.MethodGet}
	}
	return response.Header.Get("ETag"), nil
}

func requireEntityTag(etag string) error {
	if etag == "" {
		return &APIError{Status: http.StatusBadGateway, Title: "Invalid control-plane response", Detail: "The administrative service omitted the resource version."}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func itemPath(collection, id string) string {
	return "/v1/" + collection + "/" + url.PathEscape(id)
}

type usersResponse struct {
	Data []controlplane.User `json:"data"`
}

func (c *DaprAPI) ListUsers(ctx context.Context) ([]controlplane.User, error) {
	var result usersResponse
	_, err := c.invoke(ctx, c.resourceAppID, http.MethodGet, "/v1/users", "", nil, &result)
	return result.Data, err
}

func (c *DaprAPI) GetUser(ctx context.Context, id string) (Versioned[controlplane.User], error) {
	var result controlplane.User
	etag, err := c.invoke(ctx, c.resourceAppID, http.MethodGet, itemPath("users", id), "", nil, &result)
	if err == nil {
		err = requireEntityTag(etag)
	}
	return Versioned[controlplane.User]{Value: result, ETag: etag}, err
}

func (c *DaprAPI) CreateUser(ctx context.Context, input controlplane.UserInput) (controlplane.User, error) {
	var result controlplane.User
	_, err := c.invokeWithoutRetries(ctx, c.resourceAppID, http.MethodPost, "/v1/users", "", input, &result)
	return result, err
}

func (c *DaprAPI) UpdateUser(ctx context.Context, id string, input controlplane.UserInput, ifMatch string) (Versioned[controlplane.User], error) {
	var result controlplane.User
	etag, err := c.invokeWithoutRetries(ctx, c.resourceAppID, http.MethodPut, itemPath("users", id), ifMatch, input, &result)
	if err == nil {
		err = requireEntityTag(etag)
	}
	return Versioned[controlplane.User]{Value: result, ETag: etag}, err
}

func (c *DaprAPI) DeleteUser(ctx context.Context, id, ifMatch string) error {
	_, err := c.invokeWithoutRetries(ctx, c.resourceAppID, http.MethodDelete, itemPath("users", id), ifMatch, nil, nil)
	return err
}

type providersResponse struct {
	Data []controlplane.Provider `json:"data"`
}

func (c *DaprAPI) ListProviders(ctx context.Context) ([]controlplane.Provider, error) {
	var result providersResponse
	_, err := c.invoke(ctx, c.resourceAppID, http.MethodGet, "/v1/providers", "", nil, &result)
	return result.Data, err
}

func (c *DaprAPI) GetProvider(ctx context.Context, id string) (Versioned[controlplane.Provider], error) {
	var result controlplane.Provider
	etag, err := c.invoke(ctx, c.resourceAppID, http.MethodGet, itemPath("providers", id), "", nil, &result)
	if err == nil {
		err = requireEntityTag(etag)
	}
	return Versioned[controlplane.Provider]{Value: result, ETag: etag}, err
}

func (c *DaprAPI) CreateProvider(ctx context.Context, input controlplane.ProviderInput) (controlplane.Provider, error) {
	var result controlplane.Provider
	_, err := c.invokeWithoutRetries(ctx, c.resourceAppID, http.MethodPost, "/v1/providers", "", input, &result)
	return result, err
}

func (c *DaprAPI) UpdateProvider(ctx context.Context, id string, input controlplane.ProviderInput, ifMatch string) (Versioned[controlplane.Provider], error) {
	var result controlplane.Provider
	etag, err := c.invokeWithoutRetries(ctx, c.resourceAppID, http.MethodPut, itemPath("providers", id), ifMatch, input, &result)
	if err == nil {
		err = requireEntityTag(etag)
	}
	return Versioned[controlplane.Provider]{Value: result, ETag: etag}, err
}

func (c *DaprAPI) DeleteProvider(ctx context.Context, id, ifMatch string) error {
	_, err := c.invokeWithoutRetries(ctx, c.resourceAppID, http.MethodDelete, itemPath("providers", id), ifMatch, nil, nil)
	return err
}

type modelsResponse struct {
	Data []controlplane.Model `json:"data"`
}

func (c *DaprAPI) ListModels(ctx context.Context) ([]controlplane.Model, error) {
	var result modelsResponse
	_, err := c.invoke(ctx, c.resourceAppID, http.MethodGet, "/v1/models", "", nil, &result)
	return result.Data, err
}

func (c *DaprAPI) GetModel(ctx context.Context, id string) (Versioned[controlplane.Model], error) {
	var result controlplane.Model
	etag, err := c.invoke(ctx, c.resourceAppID, http.MethodGet, itemPath("models", id), "", nil, &result)
	if err == nil {
		err = requireEntityTag(etag)
	}
	return Versioned[controlplane.Model]{Value: result, ETag: etag}, err
}

func (c *DaprAPI) CreateModel(ctx context.Context, input controlplane.ModelInput) (controlplane.Model, error) {
	var result controlplane.Model
	_, err := c.invokeWithoutRetries(ctx, c.resourceAppID, http.MethodPost, "/v1/models", "", input, &result)
	return result, err
}

func (c *DaprAPI) UpdateModel(ctx context.Context, id string, input controlplane.ModelInput, ifMatch string) (Versioned[controlplane.Model], error) {
	var result controlplane.Model
	etag, err := c.invokeWithoutRetries(ctx, c.resourceAppID, http.MethodPut, itemPath("models", id), ifMatch, input, &result)
	if err == nil {
		err = requireEntityTag(etag)
	}
	return Versioned[controlplane.Model]{Value: result, ETag: etag}, err
}

func (c *DaprAPI) DeleteModel(ctx context.Context, id, ifMatch string) error {
	_, err := c.invokeWithoutRetries(ctx, c.resourceAppID, http.MethodDelete, itemPath("models", id), ifMatch, nil, nil)
	return err
}

type virtualKeysResponse struct {
	Data []controlplane.PublicVirtualKey `json:"data"`
}

func (c *DaprAPI) ListVirtualKeys(ctx context.Context) ([]controlplane.PublicVirtualKey, error) {
	var result virtualKeysResponse
	_, err := c.invoke(ctx, c.virtualKeyAppID, http.MethodGet, "/v1/virtual-keys", "", nil, &result)
	return result.Data, err
}

func (c *DaprAPI) GetVirtualKey(ctx context.Context, id string) (Versioned[controlplane.PublicVirtualKey], error) {
	var result controlplane.PublicVirtualKey
	etag, err := c.invoke(ctx, c.virtualKeyAppID, http.MethodGet, itemPath("virtual-keys", id), "", nil, &result)
	if err == nil {
		err = requireEntityTag(etag)
	}
	return Versioned[controlplane.PublicVirtualKey]{Value: result, ETag: etag}, err
}

func (c *DaprAPI) CreateVirtualKey(ctx context.Context, input controlplane.VirtualKeyInput) (controlplane.CreatedVirtualKey, error) {
	var result controlplane.CreatedVirtualKey
	_, err := c.invokeWithoutRetries(ctx, c.virtualKeyAppID, http.MethodPost, "/v1/virtual-keys", "", input, &result)
	return result, err
}

func (c *DaprAPI) UpdateVirtualKey(ctx context.Context, id string, input controlplane.VirtualKeyInput, ifMatch string) (Versioned[controlplane.PublicVirtualKey], error) {
	var result controlplane.PublicVirtualKey
	etag, err := c.invokeWithoutRetries(ctx, c.virtualKeyAppID, http.MethodPut, itemPath("virtual-keys", id), ifMatch, input, &result)
	if err == nil {
		err = requireEntityTag(etag)
	}
	return Versioned[controlplane.PublicVirtualKey]{Value: result, ETag: etag}, err
}

func (c *DaprAPI) DeleteVirtualKey(ctx context.Context, id, ifMatch string) error {
	_, err := c.invokeWithoutRetries(ctx, c.virtualKeyAppID, http.MethodDelete, itemPath("virtual-keys", id), ifMatch, nil, nil)
	return err
}
