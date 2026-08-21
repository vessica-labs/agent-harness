package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestConcurrentClientsReloadRotatedCredentialsUnderProfileLock(t *testing.T) {
	now := time.Now().UTC()
	persisted := sessionCredentials{AccessToken: "access-old", RefreshToken: "refresh-old", AccessExpiresAt: now.Add(-time.Minute).Format(time.RFC3339), RefreshExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
	var stateMu sync.Mutex
	refreshCalls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		refreshCalls++
		if input["refresh_token"] != "refresh-old" || refreshCalls != 1 {
			http.Error(w, `{"error":"refresh token reuse"}`, http.StatusUnauthorized)
			return
		}
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": sessionCredentials{AccessToken: "access-new", RefreshToken: "refresh-new", AccessExpiresAt: now.Add(time.Hour).Format(time.RFC3339), RefreshExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339)}})
	}))
	defer host.Close()

	load := func() (sessionCredentials, error) {
		stateMu.Lock()
		defer stateMu.Unlock()
		return persisted, nil
	}
	persist := func(value sessionCredentials) error {
		stateMu.Lock()
		defer stateMu.Unlock()
		persisted = value
		return nil
	}
	newClient := func() *apiClient {
		return &apiClient{
			url: host.URL, token: "access-old", refreshToken: "refresh-old", accessExpiresAt: now.Add(-time.Minute), refreshExpiresAt: now.Add(time.Hour), profileName: "concurrent-refresh-test", http: host.Client(),
			loadLatest: load, persist: persist,
			lockRefresh: func(ctx context.Context) (func(), error) {
				return acquireProfileRefreshLock(ctx, "concurrent-refresh-test")
			},
		}
	}
	clients := []*apiClient{newClient(), newClient()}
	start := make(chan struct{})
	errorsByClient := make(chan error, len(clients))
	var workers sync.WaitGroup
	for _, client := range clients {
		workers.Add(1)
		go func(client *apiClient) {
			defer workers.Done()
			<-start
			errorsByClient <- client.ensureToken(context.Background())
		}(client)
	}
	close(start)
	workers.Wait()
	close(errorsByClient)
	for err := range errorsByClient {
		if err != nil {
			t.Fatal(err)
		}
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	if persisted.RefreshToken != "refresh-new" {
		t.Fatalf("persisted refresh token = %q, want rotated token", persisted.RefreshToken)
	}
	for index, client := range clients {
		if client.accessToken() != "access-new" {
			t.Fatalf("client %d did not adopt rotated access token", index)
		}
	}
}
