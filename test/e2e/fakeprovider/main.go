// Command fakeprovider is a deterministic Anthropic Messages API stub used by
// the k3s smoke test. It is never part of a production image.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	listen := flag.String("listen", ":18082", "HTTP listen address")
	flag.Parse()
	expectedKey := os.Getenv("FAKE_ANTHROPIC_API_KEY")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if expectedKey != "" && r.Header.Get("x-api-key") != expectedKey {
			http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid key"}}`, http.StatusUnauthorized)
			return
		}
		var request struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []any  `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model == "" || request.MaxTokens <= 0 || len(request.Messages) == 0 {
			http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"invalid request"}}`, http.StatusBadRequest)
			return
		}
		responseText := "gwai e2e ok (" + request.Model + ")"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_gwai_e2e", "type": "message", "role": "assistant", "model": request.Model,
			"content":     []map[string]string{{"type": "text", "text": responseText}},
			"stop_reason": "end_turn", "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": 7, "output_tokens": 4},
		})
	})

	server := &http.Server{
		Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	log.Printf("fake Anthropic provider listening on %s", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
