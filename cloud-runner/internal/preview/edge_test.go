package preview

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestEdgeRejectsPublicUpstreams(t *testing.T) {
	for _, upstream := range []string{
		"https://control-plane.railway.internal", // https, not private http
		"http://example.com",
		"http://10.0.0.5:8080",
		"",
		"not a url",
	} {
		if _, err := NewEdgeHandler(upstream, "token"); err == nil {
			t.Fatalf("upstream %q should be rejected", upstream)
		}
	}
}

func TestEdgeRequiresToken(t *testing.T) {
	if _, err := NewEdgeHandler("http://127.0.0.1:8080", "  "); err == nil {
		t.Fatal("blank edge token should be rejected")
	}
}

func TestEdgeOverwritesTokenHeaderAndForwards(t *testing.T) {
	var received http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	parsed, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewEdgeHandler("http://127.0.0.1:"+parsed.Port(), "edge-secret")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://previews.example.com/previews/run-1/", nil)
	request.Header.Set(EdgeHeader, "attacker-chosen")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if received.Get(EdgeHeader) != "edge-secret" {
		t.Fatalf("edge header = %q, want the shared token", received.Get(EdgeHeader))
	}
	if received.Get("X-Forwarded-Host") != "previews.example.com" {
		t.Fatalf("forwarded host = %q", received.Get("X-Forwarded-Host"))
	}
}

func TestEdgeHealthEndpointIsLocal(t *testing.T) {
	handler, err := NewEdgeHandler("http://127.0.0.1:1", "token")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", recorder.Code)
	}
}
