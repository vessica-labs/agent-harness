package linearapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const OAuthTokenURL = "https://api.linear.app/oauth/token"

type OAuthCredential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

func (c OAuthCredential) NeedsRefresh(now time.Time) bool {
	return !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(now.Add(2*time.Minute))
}

func RefreshOAuth(ctx context.Context, client *http.Client, endpoint string, credential OAuthCredential, now time.Time) (OAuthCredential, error) {
	if credential.RefreshToken == "" || credential.ClientID == "" {
		return credential, errors.New("Linear OAuth refresh token and client id are required")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if endpoint == "" {
		endpoint = OAuthTokenURL
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credential.RefreshToken}, "client_id": {credential.ClientID}}
	if credential.ClientSecret != "" {
		form.Set("client_secret", credential.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return credential, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return credential, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return credential, err
	}
	if response.StatusCode != http.StatusOK {
		return credential, fmt.Errorf("Linear OAuth refresh returned %d", response.StatusCode)
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return credential, err
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresIn <= 0 {
		return credential, errors.New("Linear OAuth refresh response is incomplete")
	}
	credential.AccessToken = token.AccessToken
	credential.RefreshToken = token.RefreshToken
	credential.ExpiresAt = now.Add(time.Duration(token.ExpiresIn) * time.Second)
	return credential, nil
}

func ParseExpiry(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return now.Add(time.Duration(seconds) * time.Second), nil
	}
	return time.Parse(time.RFC3339, value)
}
