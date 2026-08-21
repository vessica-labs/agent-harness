package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/linearapi"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
)

const linearLocalCallback = "http://127.0.0.1:8743/callback"

func linearAuth(ctx context.Context, client *apiClient, args []string) error {
	if len(args) > 0 && args[0] == "manifest" {
		flags := flag.NewFlagSet("cloud auth linear manifest", flag.ContinueOnError)
		publicURL := flags.String("url", "", "public control-plane URL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !strings.HasPrefix(*publicURL, "https://") {
			return errors.New("manifest --url must be the public HTTPS control-plane URL")
		}
		values := url.Values{
			"distribution": {"private"}, "display.description": {"Vessica runs repository-owned coding pipelines for delegated Linear issues"},
			"developer.name": {"Vessica Labs"}, "oauth.client_name": {"Vessica"},
			"oauth.client_uri":    {"https://github.com/vessica-labs/agent-harness"},
			"oauth.redirect_uris": {linearLocalCallback}, "oauth.grant_types": {"authorization_code"},
			"webhook.enabled": {"true"}, "webhook.url": {strings.TrimRight(*publicURL, "/") + "/webhooks/linear"},
			"webhook.resourceTypes": {"AgentSessionEvent", "Issue", "Comment", "OAuthAuthorization", "PermissionChange"},
		}
		manifestURL := "https://linear.app/settings/api/applications/new?" + values.Encode()
		if err := openBrowser(manifestURL); err != nil {
			fmt.Println(manifestURL)
		}
		fmt.Println("Create the app, record its client ID, client secret, and webhook signing secret, then run cloud auth linear with those values.")
		return nil
	}
	flags := flag.NewFlagSet("cloud auth linear", flag.ContinueOnError)
	clientID := flags.String("client-id", os.Getenv("LINEAR_CLIENT_ID"), "Linear OAuth client id")
	clientSecret := flags.String("client-secret", os.Getenv("LINEAR_CLIENT_SECRET"), "Linear OAuth client secret")
	webhookSecret := flags.String("webhook-secret", os.Getenv("LINEAR_WEBHOOK_SECRET"), "Linear webhook signing secret")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if access := os.Getenv("LINEAR_ACCESS_TOKEN"); access != "" {
		expiresAt, err := linearapi.ParseExpiry(os.Getenv("LINEAR_EXPIRES_AT"), time.Now().UTC())
		if err != nil {
			return fmt.Errorf("LINEAR_EXPIRES_AT: %w", err)
		}
		credential := linearapi.OAuthCredential{AccessToken: access, RefreshToken: os.Getenv("LINEAR_REFRESH_TOKEN"),
			ClientID: *clientID, ClientSecret: *clientSecret, ExpiresAt: expiresAt}
		return storeLinearCredential(ctx, client, credential, *webhookSecret)
	}
	if *clientID == "" || *clientSecret == "" || *webhookSecret == "" {
		return errors.New("provide --client-id, --client-secret, and --webhook-secret, or set the corresponding LINEAR_* variables")
	}
	credential, err := runLinearOAuth(ctx, *clientID, *clientSecret)
	if err != nil {
		return err
	}
	return storeLinearCredential(ctx, client, credential, *webhookSecret)
}

func storeLinearCredential(ctx context.Context, client *apiClient, credential linearapi.OAuthCredential, webhookSecret string) error {
	if credential.AccessToken == "" || webhookSecret == "" {
		return errors.New("Linear access token and webhook secret are required")
	}
	if err := putCredential(ctx, client, "linear_oauth", string(mustMarshal(credential))); err != nil {
		return err
	}
	return putCredential(ctx, client, "linear_webhook_secret", webhookSecret)
}

func runLinearOAuth(ctx context.Context, clientID, clientSecret string) (linearapi.OAuthCredential, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:8743")
	if err != nil {
		return linearapi.OAuthCredential{}, errors.New("Linear OAuth callback port 8743 is unavailable")
	}
	defer listener.Close()
	state, _ := secure.GenerateKey()
	type result struct {
		code string
		err  error
	}
	completed := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		if !secure.EqualSecret(r.URL.Query().Get("state"), state) {
			completed <- result{err: errors.New("Linear OAuth state mismatch")}
			http.Error(w, "Invalid state", http.StatusUnauthorized)
			return
		}
		completed <- result{code: r.URL.Query().Get("code")}
		_, _ = io.WriteString(w, "Linear authorized. You may close this window and return to Codex.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)
	defer server.Shutdown(context.Background())
	authorize := url.Values{"client_id": {clientID}, "redirect_uri": {linearLocalCallback}, "response_type": {"code"},
		"scope": {"read,write,issues:create,comments:create,app:assignable"}, "state": {state}, "actor": {"app"}, "prompt": {"consent"}}
	if err := openBrowser("https://linear.app/oauth/authorize?" + authorize.Encode()); err != nil {
		return linearapi.OAuthCredential{}, err
	}
	select {
	case <-ctx.Done():
		return linearapi.OAuthCredential{}, ctx.Err()
	case value := <-completed:
		if value.err != nil {
			return linearapi.OAuthCredential{}, value.err
		}
		if value.code == "" {
			return linearapi.OAuthCredential{}, errors.New("Linear OAuth callback omitted code")
		}
		return exchangeLinearCode(ctx, clientID, clientSecret, value.code)
	case <-time.After(10 * time.Minute):
		return linearapi.OAuthCredential{}, errors.New("Linear OAuth flow timed out")
	}
}

func exchangeLinearCode(ctx context.Context, clientID, clientSecret, code string) (linearapi.OAuthCredential, error) {
	form := url.Values{"code": {code}, "redirect_uri": {linearLocalCallback}, "client_id": {clientID}, "client_secret": {clientSecret}, "grant_type": {"authorization_code"}}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, linearapi.OAuthTokenURL, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return linearapi.OAuthCredential{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return linearapi.OAuthCredential{}, fmt.Errorf("Linear OAuth exchange returned %d", response.StatusCode)
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil {
		return linearapi.OAuthCredential{}, err
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresIn <= 0 {
		return linearapi.OAuthCredential{}, errors.New("Linear OAuth exchange response is incomplete")
	}
	return linearapi.OAuthCredential{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		ClientID: clientID, ClientSecret: clientSecret, ExpiresAt: time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second)}, nil
}
