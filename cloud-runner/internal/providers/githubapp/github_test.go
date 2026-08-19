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
