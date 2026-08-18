package linearapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshOAuthRotatesAndPreservesClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || string(body) == "" {
			t.Fatal("invalid refresh request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":86400}`))
	}))
	defer server.Close()
	now := time.Now().UTC()
	credential, err := RefreshOAuth(context.Background(), server.Client(), server.URL, OAuthCredential{
		AccessToken: "old", RefreshToken: "old-refresh", ClientID: "client", ClientSecret: "secret",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "new-access" || credential.RefreshToken != "new-refresh" || credential.ClientID != "client" {
		t.Fatalf("unexpected credential: %+v", credential)
	}
	if credential.ExpiresAt.Before(now.Add(23 * time.Hour)) {
		t.Fatal("expiry was not advanced")
	}
}
