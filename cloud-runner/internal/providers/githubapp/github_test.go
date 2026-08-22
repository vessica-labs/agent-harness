package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUpdateWebhookConfigUsesAppJWTAndDoesNotReturnSecret(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/app/hook/config" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatal("GitHub App JWT was not supplied")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["url"] != "https://runner.example/webhooks/github" || body["secret"] != "generated-secret" || body["content_type"] != "json" {
			t.Fatalf("unexpected webhook configuration: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://runner.example/webhooks/github","content_type":"json","insecure_ssl":"0"}`))
	}))
	defer host.Close()

	client := New(Credentials{AppID: 123, PrivateKey: privateKey})
	client.baseURL = host.URL
	client.http = host.Client()
	client.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	config, err := client.UpdateWebhookConfig(context.Background(), "https://runner.example/webhooks/github", "generated-secret")
	if err != nil {
		t.Fatal(err)
	}
	if config.URL != "https://runner.example/webhooks/github" || config.ContentType != "json" {
		t.Fatalf("unexpected result: %+v", config)
	}
}

func TestMintInstallationTokenRequestsRepositoryAndWorkflowWrites(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/456/access_tokens" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Repositories) != 1 || body.Repositories[0] != "vessica" {
			t.Fatalf("repositories = %#v", body.Repositories)
		}
		want := map[string]string{"metadata": "read", "contents": "write", "pull_requests": "write", "workflows": "write"}
		if len(body.Permissions) != len(want) {
			t.Fatalf("permissions = %#v, want %#v", body.Permissions, want)
		}
		for permission, access := range want {
			if body.Permissions[permission] != access {
				t.Fatalf("permission %q = %q, want %q", permission, body.Permissions[permission], access)
			}
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"2027-01-01T00:00:00Z"}`))
	}))
	defer host.Close()

	client := New(Credentials{AppID: 123, PrivateKey: privateKey})
	client.baseURL = host.URL
	client.http = host.Client()
	client.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	token, err := client.MintInstallationToken(context.Background(), 456, "vessica-labs", "vessica")
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "installation-token" {
		t.Fatalf("token = %q", token.Token)
	}
}
