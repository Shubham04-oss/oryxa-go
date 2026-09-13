package observability

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogLevel(t *testing.T) {
	if logLevel("debug") != slog.LevelDebug || logLevel("warning") != slog.LevelWarn ||
		logLevel("ERROR") != slog.LevelError || logLevel("unknown") != slog.LevelInfo {
		t.Fatal("unexpected log-level mapping")
	}
}

func TestHTTPHandlerAddsRequestIDAndPreservesResponse(t *testing.T) {
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/items/{id}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte("ok"))
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/items/item-1?token=must-not-be-logged", nil)
	request.Header.Set("X-Request-ID", "attacker-controlled")
	recorder := httptest.NewRecorder()
	HTTPHandler("oryxa-test", mux).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || recorder.Body.String() != "ok" || recorder.Header().Get("X-Request-ID") == "" ||
		recorder.Header().Get("X-Request-ID") == "attacker-controlled" {
		t.Fatalf("response = %d %q request-id=%q", recorder.Code, recorder.Body.String(), recorder.Header().Get("X-Request-ID"))
	}
	if output := logs.String(); strings.Contains(output, "must-not-be-logged") || strings.Contains(output, "item-1") ||
		!strings.Contains(output, `"http.route":"GET /v1/items/{id}"`) {
		t.Fatalf("access log was unsafe or unstructured: %s", output)
	}
}
