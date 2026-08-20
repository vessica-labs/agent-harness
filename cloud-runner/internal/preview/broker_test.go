package preview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestBroker(t *testing.T, ttl time.Duration, backend http.Handler) (*Broker, func()) {
	t.Helper()
	upstream := httptest.NewServer(backend)
	broker := NewBroker(ttl)
	if err := broker.Register("run-1", upstream.URL, nil); err != nil {
		t.Fatal(err)
	}
	return broker, upstream.Close
}

func htmlBackend() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app.js" {
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte("console.log('asset')"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><h1>app</h1></body></html>"))
	})
}

func TestCapabilityFlowInjectsOverlayAndSetsCookie(t *testing.T) {
	broker, closeUpstream := newTestBroker(t, time.Hour, htmlBackend())
	defer closeUpstream()
	broker.SetOverlayProvider(Overlay)
	token, _, err := broker.Issue("run-1", time.Now().Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	broker.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/previews/run-1/?cap="+token, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "harness-preview-panel") {
		t.Fatal("overlay was not injected into the HTML response")
	}
	cookie := recorder.Result().Cookies()
	if len(cookie) != 1 || cookie[0].Name != CookieName || cookie[0].Value != token || !cookie[0].HttpOnly {
		t.Fatalf("expected an http-only preview cookie, got %+v", cookie)
	}
}

func TestCookieServesRootRelativeAssets(t *testing.T) {
	broker, closeUpstream := newTestBroker(t, time.Hour, htmlBackend())
	defer closeUpstream()
	token, _, err := broker.Issue("run-1", time.Now().Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	recorder := httptest.NewRecorder()
	broker.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "asset") {
		t.Fatalf("asset request failed: %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestInvalidAndMismatchedCapabilitiesAreRejected(t *testing.T) {
	broker, closeUpstream := newTestBroker(t, time.Hour, htmlBackend())
	defer closeUpstream()
	token, _, err := broker.Issue("run-1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/previews/run-1/?cap=bogus", "/previews/other-run/?cap=" + token, "/previews/run-1/"} {
		recorder := httptest.NewRecorder()
		broker.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("path %s: status = %d, want 401", path, recorder.Code)
		}
	}
}

func TestSlidingExpiryIsClampedToDeadline(t *testing.T) {
	broker := NewBroker(time.Hour)
	if err := broker.Register("run-1", "http://127.0.0.1:1", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Minute)
	token, expires, err := broker.Issue("run-1", deadline)
	if err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(deadline) {
		t.Fatalf("initial expiry %v should clamp to deadline %v", expires, deadline)
	}
	var extended time.Time
	broker.SetActivityCallback(func(_ string, expiresAt time.Time) { extended = expiresAt })
	if runID, ok := broker.touch(token); !ok || runID != "run-1" {
		t.Fatal("touch should validate the capability")
	}
	if extended.After(deadline) {
		t.Fatalf("sliding expiry %v extended past the hard deadline %v", extended, deadline)
	}
}

func TestExpiredCapabilityIsRemoved(t *testing.T) {
	broker := NewBroker(time.Hour)
	if err := broker.RestoreCapability("stale", "run-1", time.Now().Add(-time.Minute), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok := broker.touch("stale"); ok {
		t.Fatal("expired capability should be rejected")
	}
	if _, ok := broker.capabilities["stale"]; ok {
		t.Fatal("expired capability should be deleted")
	}
}

func TestRemoveCancelsForwardAndReturnsGone(t *testing.T) {
	broker, closeUpstream := newTestBroker(t, time.Hour, htmlBackend())
	defer closeUpstream()
	cancelled := false
	_, cancel := context.WithCancel(context.Background())
	if err := broker.Register("run-1", "http://127.0.0.1:1", func() { cancelled = true; cancel() }); err != nil {
		t.Fatal(err)
	}
	token, _, err := broker.Issue("run-1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	broker.Remove("run-1")
	if !cancelled {
		t.Fatal("Remove should stop the forward")
	}
	if broker.Registered("run-1") {
		t.Fatal("target should be gone")
	}
	if _, ok := broker.touch(token); ok {
		t.Fatal("capabilities for a removed target should be revoked")
	}
}

func TestPanelRouteIsServedByBrokerNotProxy(t *testing.T) {
	broker, closeUpstream := newTestBroker(t, time.Hour, htmlBackend())
	defer closeUpstream()
	broker.SetPanelHandler(PanelHandler(func(runID string) (PanelData, bool) {
		return PanelData{RunID: runID, IssueKey: "ENG-42", PullRequestURL: "https://github.com/acme/web/pull/7"}, true
	}))
	token, _, err := broker.Issue("run-1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/previews/run-1/"+ReservedPrefix+"/panel", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	recorder := httptest.NewRecorder()
	broker.ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "ENG-42") || !strings.Contains(body, "pull/7") {
		t.Fatalf("panel response: %d %q", recorder.Code, body)
	}
	if strings.Contains(body, "<h1>app</h1>") {
		t.Fatal("panel route must not be proxied to the application")
	}
}
