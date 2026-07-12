package daprhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInvokeMethodPreservesVerbRouteQueryHeadersAndBody(t *testing.T) {
	tests := []string{http.MethodGet, http.MethodPut, http.MethodDelete}
	for _, method := range tests {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method {
					t.Errorf("method = %s, want %s", r.Method, method)
				}
				if r.URL.EscapedPath() != "/v1.0/invoke/admin-app/method/v1/resources/item" || r.URL.Query().Get("view") != "full" {
					t.Errorf("URL = %s", r.URL.String())
				}
				if r.Header.Get("Authorization") != "Bearer admin" {
					t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
				}
				if r.Header.Get("dapr-api-token") != "trusted-sidecar-token" {
					t.Errorf("dapr-api-token = %q", r.Header.Get("dapr-api-token"))
				}
				payload, _ := io.ReadAll(r.Body)
				if string(payload) != `{"value":1}` {
					t.Errorf("body = %q", payload)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			client := New(server.URL, "trusted-sidecar-token", server.Client())
			headers := make(http.Header)
			headers.Set("Authorization", "Bearer admin")
			headers.Set("dapr-api-token", "spoofed")
			response, err := client.InvokeMethod(
				context.Background(), method, "admin-app", "/v1/resources/item?view=full",
				bytes.NewBufferString(`{"value":1}`), headers,
			)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
		})
	}
}

func TestLegacyInvokeAndInvokeJSONRemainPOSTCompatible(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if calls == 1 {
			if r.Header.Get("Content-Type") != "text/plain" {
				t.Errorf("content type = %q", r.Header.Get("Content-Type"))
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["input"] != "ok" {
			t.Errorf("JSON input = %#v, err = %v", input, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":"ok"}`))
	}))
	defer server.Close()

	client := New(server.URL, "", server.Client())
	response, err := client.Invoke(context.Background(), "app", "/plain", bytes.NewBufferString("body"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	var output map[string]string
	if err := client.InvokeJSON(context.Background(), "app", "/json", map[string]string{"input": "ok"}, &output); err != nil {
		t.Fatal(err)
	}
	if output["output"] != "ok" {
		t.Fatalf("JSON output = %#v", output)
	}
}

func TestInvokeMethodDoesNotFollowRedirectOrLeakTokens(t *testing.T) {
	leaked := make(chan http.Header, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer sidecar.Close()

	client := New(sidecar.URL, "sidecar-secret", sidecar.Client())
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer admin-secret")
	response, err := client.InvokeMethod(context.Background(), http.MethodGet, "app", "/v1/users", nil, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect status = %d", response.StatusCode)
	}
	select {
	case headers := <-leaked:
		t.Fatalf("redirect target was contacted with headers: %v", headers)
	default:
	}
}
