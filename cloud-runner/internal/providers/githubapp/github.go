package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type Credentials struct {
	AppID         int64  `json:"app_id"`
	PrivateKey    string `json:"private_key"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type WebhookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	InsecureSSL string `json:"insecure_ssl"`
}

type Client struct {
	credentials Credentials
	baseURL     string
	http        *http.Client
	now         func() time.Time
}

func New(credentials Credentials) *Client {
	return &Client{credentials: credentials, baseURL: "https://api.github.com",
		http: &http.Client{Timeout: 20 * time.Second}, now: time.Now}
}

// UpdateWebhookConfig configures the webhook belonging to an existing GitHub
// App. The caller is responsible for persisting the same secret only after
// GitHub accepts it.
func (c *Client) UpdateWebhookConfig(ctx context.Context, webhookURL, secret string) (WebhookConfig, error) {
	if webhookURL == "" || secret == "" {
		return WebhookConfig{}, errors.New("GitHub webhook URL and secret are required")
	}
	jwt, err := c.jwt()
	if err != nil {
		return WebhookConfig{}, err
	}
	body, _ := json.Marshal(map[string]string{
		"url": webhookURL, "content_type": "json", "secret": secret, "insecure_ssl": "0",
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+"/app/hook/config", bytes.NewReader(body))
	if err != nil {
		return WebhookConfig{}, err
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "agent-harness-control-plane")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return WebhookConfig{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return WebhookConfig{}, err
	}
	if response.StatusCode != http.StatusOK {
		return WebhookConfig{}, fmt.Errorf("GitHub App webhook update failed: status %d", response.StatusCode)
	}
	var result WebhookConfig
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return WebhookConfig{}, err
	}
	if result.URL == "" {
		return WebhookConfig{}, errors.New("GitHub returned an empty webhook URL")
	}
	return result, nil
}

func (c *Client) MintInstallationToken(ctx context.Context, installationID int64, owner, repo string) (Token, error) {
	jwt, err := c.jwt()
	if err != nil {
		return Token{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"repositories": []string{repo},
		"permissions":  map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"},
	})
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.baseURL, installationID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Token{}, err
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "agent-harness-control-plane")
	response, err := c.http.Do(request)
	if err != nil {
		return Token{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Token{}, err
	}
	if response.StatusCode != http.StatusCreated {
		return Token{}, fmt.Errorf("GitHub installation token request failed: status %d", response.StatusCode)
	}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return Token{}, err
	}
	if result.Token == "" {
		return Token{}, errors.New("GitHub returned an empty installation token")
	}
	_ = owner
	return Token(result), nil
}

func (c *Client) jwt() (string, error) {
	key, err := parsePrivateKey([]byte(c.credentials.PrivateKey))
	if err != nil {
		return "", err
	}
	now := c.now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(c.credentials.AppID, 10)})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func parsePrivateKey(value []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(value)
	if block == nil {
		return nil, errors.New("GitHub App private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("GitHub App private key is not PKCS#1 or PKCS#8")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App private key is not RSA")
	}
	return key, nil
}
