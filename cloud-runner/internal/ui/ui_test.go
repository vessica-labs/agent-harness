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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("missing proxy authorization: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/v1/runs" {
			t.Fatalf("unexpected proxy path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	defer upstream.Close()
	server, err := New("127.0.0.1:0", upstream.URL, token, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	root := httptest.NewRecorder()
	server.Handler().ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(root.Body.String(), token) {
		t.Fatal("management token was exposed to browser HTML")
	}
	proxy := httptest.NewRecorder()
	server.Handler().ServeHTTP(proxy, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
	if proxy.Code != http.StatusOK {
		body, _ := io.ReadAll(proxy.Result().Body)
		t.Fatalf("proxy failed: %d %s", proxy.Code, body)
	}
}

func TestLocalUIRejectsPublicBind(t *testing.T) {
	if _, err := New("0.0.0.0:7373", "https://example.com", "token", slog.Default()); err == nil {
		t.Fatal("public UI bind accepted")
	}
}
