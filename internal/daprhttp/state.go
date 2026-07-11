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

	"github.com/Albe83/gwai/internal/state"
)

type StateStore struct {
	client *Client
	name   string
}

func NewStateStore(client *Client, name string) *StateStore {
	return &StateStore{client: client, name: name}
}

func (s *StateStore) Get(ctx context.Context, key string) (state.Entry, error) {
	endpoint := "/v1.0/state/" + url.PathEscape(s.name) + "/" + url.PathEscape(key) + "?consistency=strong"
	request, err := s.client.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return state.Entry{}, fmt.Errorf("create Dapr state request: %w", err)
	}
	response, err := s.client.http.Do(request)
	if err != nil {
		return state.Entry{}, fmt.Errorf("get Dapr state %q: %w", key, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotFound {
		return state.Entry{}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return state.Entry{}, &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return state.Entry{}, fmt.Errorf("read Dapr state %q: %w", key, err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return state.Entry{}, nil
	}
	if !json.Valid(body) {
		return state.Entry{}, fmt.Errorf("Dapr state %q is not valid JSON", key)
	}
	return state.Entry{Value: body, ETag: response.Header.Get("ETag"), Exists: true}, nil
}

type transactionRequest struct {
	Operations []transactionOperation `json:"operations"`
}

type transactionOperation struct {
	Operation string                  `json:"operation"`
	Request   transactionStateRequest `json:"request"`
}

type transactionStateRequest struct {
	Key     string          `json:"key"`
	Value   json.RawMessage `json:"value,omitempty"`
	ETag    string          `json:"etag,omitempty"`
	Options *stateOptions   `json:"options,omitempty"`
}

type stateOptions struct {
	Concurrency string `json:"concurrency"`
	Consistency string `json:"consistency"`
}

func (s *StateStore) Transact(ctx context.Context, operations []state.Operation) error {
	requestBody := transactionRequest{Operations: make([]transactionOperation, 0, len(operations))}
	for _, operation := range operations {
		request := transactionStateRequest{Key: operation.Key, Value: operation.Value, ETag: operation.ETag}
		if operation.ETag != "" {
			request.Options = &stateOptions{Concurrency: "first-write", Consistency: "strong"}
		}
		requestBody.Operations = append(requestBody.Operations, transactionOperation{
			Operation: string(operation.Type),
			Request:   request,
		})
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("encode Dapr state transaction: %w", err)
	}
	endpoint := "/v1.0/state/" + url.PathEscape(s.name) + "/transaction"
	request, err := s.client.newRequest(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create Dapr state transaction request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.http.Do(request)
	if err != nil {
		return fmt.Errorf("execute Dapr state transaction: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusPreconditionFailed {
		return state.ErrConflict
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return nil
}
