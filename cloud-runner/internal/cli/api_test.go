package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWatchUsesContextRatherThanOrdinaryRequestTimeout(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 7\ndata: {\"id\":7}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer host.Close()

	client := &apiClient{url: host.URL, token: "token", http: &http.Client{Timeout: 20 * time.Millisecond}}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	started := time.Now()
	err := client.watch(ctx, "run-1", 0, &output)
	if elapsed := time.Since(started); elapsed < 90*time.Millisecond {
		t.Fatalf("watch ended at ordinary request timeout after %s: %v", elapsed, err)
	}
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("watch error = %v, want context deadline", err)
	}
	if output.String() != "{\"id\":7}\n" {
		t.Fatalf("unexpected event output %q", output.String())
	}
}
