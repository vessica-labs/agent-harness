package ui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalUIKeepsBearerTokenServerSide(t *testing.T) {
	const token = "management-token-never-in-browser"
	upstreamHost := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("missing proxy authorization: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/v1/runs" {
			t.Fatalf("unexpected proxy path: %s", r.URL.Path)
		}
		if r.Host != upstreamHost {
			t.Fatalf("unexpected proxy host: %s", r.Host)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	defer upstream.Close()
	upstreamHost = strings.TrimPrefix(upstream.URL, "http://")
	server, err := New("127.0.0.1:0", upstream.URL, token, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	root := httptest.NewRecorder()
	server.Handler().ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(root.Body.String(), token) {
		t.Fatal("management token was exposed to browser HTML")
	}
	for _, expected := range []string{"Pipeline DAG · execution order", "Run history · newest first", "pipeline complete", "codex.", "external_syncs", "estimated_api_cost_usd", "run_id="} {
		if !strings.Contains(root.Body.String(), expected) {
			t.Fatalf("runner UI is missing %q", expected)
		}
	}
	proxy := httptest.NewRecorder()
	server.Handler().ServeHTTP(proxy, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
	if proxy.Code != http.StatusOK {
		body, _ := io.ReadAll(proxy.Result().Body)
		t.Fatalf("proxy failed: %d %s", proxy.Code, body)
	}
}

func TestLocalUIPreservesRunFilterOnEventProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events" || r.URL.Query().Get("run_id") != "run-123" {
			t.Fatalf("event filter was not proxied: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("event: ready\n\n"))
	}))
	defer upstream.Close()
	server, err := New("127.0.0.1:0", upstream.URL, "token", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/events?run_id=run-123", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("event proxy status=%d", response.Code)
	}
}

func TestLocalUIRejectsPublicBind(t *testing.T) {
	if _, err := New("0.0.0.0:7373", "https://example.com", "token", slog.Default()); err == nil {
		t.Fatal("public UI bind accepted")
	}
}
