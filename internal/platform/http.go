package platform

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

type contextKey string

const requestIDKey contextKey = "request-id"

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func SecureEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

func WriteProblem(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: RequestID(r.Context()),
	})
}

func DecodeJSON(r *http.Request, destination any, maxBytes int64, rejectUnknown bool) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	reader := io.Reader(r.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(r.Body, maxBytes+1)
	}
	decoder := json.NewDecoder(reader)
	if rejectUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON body: %w", err)
	}
	if maxBytes > 0 && decoder.InputOffset() > maxBytes {
		return fmt.Errorf("request body exceeds %d bytes", maxBytes)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain a single JSON value")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

func BearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func (w *statusRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func HTTPMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID, _ = NewID("req")
		}
		w.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, requestID))
		recorder := &statusRecorder{ResponseWriter: w}

		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic while serving request", "request_id", requestID, "panic", recovered, "stack", string(debug.Stack()))
				if recorder.status == 0 {
					WriteProblem(recorder, r, http.StatusInternalServerError, "Internal Server Error", "the server could not process the request")
				}
			}
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.Info("http request",
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"bytes", recorder.bytes,
				"duration_ms", time.Since(started).Milliseconds(),
			)
		}()

		next.ServeHTTP(recorder, r)
	})
}

func NewLogger(service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{})).With("service", service)
}

func Serve(ctx context.Context, logger *slog.Logger, server *http.Server) error {
	return ServeWithShutdownTimeout(ctx, logger, server, 15*time.Second)
}

// ServeWithShutdownTimeout runs an HTTP server and lets in-flight requests
// finish for the supplied interval after SIGINT, SIGTERM, or parent-context
// cancellation.
func ServeWithShutdownTimeout(ctx context.Context, logger *slog.Logger, server *http.Server, shutdownTimeout time.Duration) error {
	if shutdownTimeout <= 0 {
		return errors.New("HTTP shutdown timeout must be positive")
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "address", server.Addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down http server: %w", err)
		}
		return nil
	}
}
