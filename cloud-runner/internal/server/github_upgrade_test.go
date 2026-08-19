package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/githubapp"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

type webhookUpdaterStub struct {
	url    string
	secret string
}

func (s *webhookUpdaterStub) UpdateWebhookConfig(_ context.Context, webhookURL, secret string) (githubapp.WebhookConfig, error) {
	s.url, s.secret = webhookURL, secret
	return githubapp.WebhookConfig{URL: webhookURL, ContentType: "json", InsecureSSL: "0"}, nil
}

func TestUpgradeGitHubWebhookReusesCredentialAndKeepsSecretOutOfResponse(t *testing.T) {
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	server := New(Config{PublicURL: "https://runner.example"}, store.NewMemory(), box, events.NewBroker(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	original, _ := json.Marshal(githubapp.Credentials{AppID: 123, PrivateKey: "private-key", WebhookSecret: "old-secret"})
	if err := server.putCredential(context.Background(), "github_app", original); err != nil {
		t.Fatal(err)
	}
	stub := &webhookUpdaterStub{}
	server.githubWebhookClient = func(githubapp.Credentials) githubWebhookUpdater { return stub }

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/github/upgrade-webhook", nil)
	server.upgradeGitHubWebhook(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if stub.url != "https://runner.example/webhooks/github" || stub.secret == "" || stub.secret == "old-secret" {
		t.Fatalf("unexpected webhook update: url=%q secret changed=%v", stub.url, stub.secret != "old-secret")
	}
	if strings.Contains(response.Body.String(), stub.secret) || strings.Contains(response.Body.String(), "old-secret") {
		t.Fatal("webhook secret leaked in response")
	}
	raw, err := server.credential(context.Background(), "github_app")
	if err != nil {
		t.Fatal(err)
	}
	var stored githubapp.Credentials
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.AppID != 123 || stored.PrivateKey != "private-key" || stored.WebhookSecret != stub.secret {
		t.Fatalf("credential was not safely merged: %+v", stored)
	}
}
