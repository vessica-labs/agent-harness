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
	for _, expected := range []string{"Pipeline DAG · execution order", "Run history · newest first", "pipeline complete", "codex.", "external_syncs", "estimated_api_cost_usd", "run_id=", "scheduleRefresh()", `type="button" class="run`, `role="log"`, `aria-pressed=`, "activityCard", "activity-payload", "Ran command", `id="back-to-runs"`, "function enterRunDetail", "runsEl.hidden=true", "detailEl.hidden=false", "function showRunList", "backToRunsEl.onclick=showRunList", "const eventSummary=e=>", "e.type==='run.infrastructure.stage'", "e.type==='stage.started'", "e.type==='stage.completed'", "function coalesceEvents", "activityIndexes", "Live updates paused", "eventSource!==source", "eventSource===source"} {
		if !strings.Contains(root.Body.String(), expected) {
			t.Fatalf("runner UI is missing %q", expected)
		}
	}
	for _, expected := range []string{`id="inbox-button"`, `id="inbox-badge"`, "Input inbox", "loadInbox()", "data-input-request", "Recommended", "Another answer", "/v1/input-requests/"} {
		if !strings.Contains(root.Body.String(), expected) {
			t.Fatalf("runner input UI is missing %q", expected)
		}
	}
	if strings.Contains(root.Body.String(), "Reconnecting…") {
		t.Fatal("routine event-stream retries should not be highlighted")
	}
	proxy := httptest.NewRecorder()
	server.Handler().ServeHTTP(proxy, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
	if proxy.Code != http.StatusOK {
		body, _ := io.ReadAll(proxy.Result().Body)
		t.Fatalf("proxy failed: %d %s", proxy.Code, body)
	}
	icon := httptest.NewRecorder()
	server.Handler().ServeHTTP(icon, httptest.NewRequest(http.MethodGet, "/assets/terminal-16.svg", nil))
	if icon.Code != http.StatusOK || !strings.Contains(icon.Header().Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("terminal icon failed: status=%d content-type=%q", icon.Code, icon.Header().Get("Content-Type"))
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
