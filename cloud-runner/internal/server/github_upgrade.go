package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/githubapp"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
)

type githubWebhookUpdater interface {
	UpdateWebhookConfig(context.Context, string, string) (githubapp.WebhookConfig, error)
}

func (s *Server) upgradeGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	publicURL := strings.TrimRight(strings.TrimSpace(s.config.PublicURL), "/")
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		writeError(w, http.StatusConflict, errors.New("HARNESS_PUBLIC_URL must be a public HTTPS origin before upgrading the GitHub webhook"))
		return
	}
	raw, err := s.credential(r.Context(), "github_app")
	if err != nil {
		writeError(w, http.StatusConflict, errors.New("GitHub App credential is not configured"))
		return
	}
	var credentials githubapp.Credentials
	if json.Unmarshal(raw, &credentials) != nil || credentials.AppID == 0 || credentials.PrivateKey == "" {
		writeError(w, http.StatusConflict, errors.New("stored GitHub App credential is invalid"))
		return
	}
	secret, err := secure.RandomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("generate GitHub webhook secret"))
		return
	}
	webhookURL := publicURL + "/webhooks/github"
	configured, err := s.githubWebhookClient(credentials).UpdateWebhookConfig(r.Context(), webhookURL, secret)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("configure existing GitHub App webhook: %w", err))
		return
	}
	credentials.WebhookSecret = secret
	updated, err := json.Marshal(credentials)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("encode updated GitHub App credential"))
		return
	}
	if err := s.putCredential(r.Context(), "github_app", updated); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("GitHub accepted the webhook configuration but the control plane could not store it; retry this command immediately"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"webhook_url":     configured.URL,
		"content_type":    configured.ContentType,
		"secret_stored":   true,
		"manual_action":   "In the GitHub App settings, enable the webhook and subscribe to Pull request events.",
		"app_reused":      true,
		"app_permissions": "unchanged",
	})
}
