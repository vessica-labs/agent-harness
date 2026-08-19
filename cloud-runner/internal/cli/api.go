package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type apiClient struct {
	url              string
	token            string
	refreshToken     string
	accessExpiresAt  time.Time
	refreshExpiresAt time.Time
	profileName      string
	mu               sync.Mutex
	http             *http.Client
}

func newAPI(profileName string) (*apiClient, error) {
	name, url, credentials, err := loadProfileSession(profileName)
	if err != nil {
		return nil, err
	}
	accessExpiry, _ := time.Parse(time.RFC3339, credentials.AccessExpiresAt)
	refreshExpiry, _ := time.Parse(time.RFC3339, credentials.RefreshExpiresAt)
	return &apiClient{url: url, token: credentials.AccessToken, refreshToken: credentials.RefreshToken, accessExpiresAt: accessExpiry, refreshExpiresAt: refreshExpiry, profileName: name, http: &http.Client{Timeout: 2 * time.Minute}}, nil
}

func (c *apiClient) do(ctx context.Context, method, path string, input, output any) error {
	var encoded []byte
	if input != nil {
		var err error
		encoded, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	if err := c.ensureToken(ctx); err != nil {
		return err
	}
	return c.doAttempt(ctx, method, path, encoded, output, true)
}

func (c *apiClient) doAttempt(ctx context.Context, method, path string, encoded []byte, output any, retry bool) error {
	var body io.Reader
	if encoded != nil {
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.url+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken())
	if encoded != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized && retry && c.hasRefreshToken() {
		_, _ = io.Copy(io.Discard, response.Body)
		if err := c.refresh(ctx, true); err != nil {
			return err
		}
		return c.doAttempt(ctx, method, path, encoded, output, false)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("control plane returned %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func (c *apiClient) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	refresh, expiry := c.refreshToken, c.accessExpiresAt
	c.mu.Unlock()
	if refresh == "" || expiry.IsZero() || time.Until(expiry) > time.Minute {
		return nil
	}
	return c.refresh(ctx, false)
}
func (c *apiClient) refresh(ctx context.Context, force bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshToken == "" {
		return errors.New("team session expired; join the control plane again")
	}
	if !force && !c.accessExpiresAt.IsZero() && time.Until(c.accessExpiresAt) > time.Minute {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": c.refreshToken})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/auth/v1/token", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("team session refresh failed (%d): %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	var result struct {
		Tokens sessionCredentials `json:"tokens"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	c.token = result.Tokens.AccessToken
	c.refreshToken = result.Tokens.RefreshToken
	c.accessExpiresAt, _ = time.Parse(time.RFC3339, result.Tokens.AccessExpiresAt)
	c.refreshExpiresAt, _ = time.Parse(time.RFC3339, result.Tokens.RefreshExpiresAt)
	return saveSessionProfile(c.profileName, c.url, result.Tokens)
}

func (c *apiClient) currentToken(ctx context.Context) (string, error) {
	if err := c.ensureToken(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, nil
}

func (c *apiClient) accessToken() string { c.mu.Lock(); defer c.mu.Unlock(); return c.token }
func (c *apiClient) hasRefreshToken() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshToken != ""
}

func (c *apiClient) download(ctx context.Context, path, destination string) error {
	if err := c.ensureToken(ctx); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken())
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("artifact download failed")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (c *apiClient) watch(ctx context.Context, runID string, after int64, output io.Writer) error {
	watchHTTP := *c.http
	watchHTTP.Timeout = 0
	for {
		if err := c.ensureToken(ctx); err != nil {
			return err
		}
		endpoint := fmt.Sprintf("%s/v1/events?after=%d", c.url, after)
		if runID != "" {
			endpoint += "&run_id=" + runID
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		token, err := c.currentToken(ctx)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := watchHTTP.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !waitForReconnect(ctx) {
				return ctx.Err()
			}
			continue
		}
		if response.StatusCode == http.StatusUnauthorized && c.hasRefreshToken() {
			response.Body.Close()
			if err := c.refresh(ctx, true); err != nil {
				return err
			}
			continue
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return fmt.Errorf("event stream returned %d", response.StatusCode)
		}
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 64<<10), 2<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "id: ") {
				if value, parseErr := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "id: ")), 10, 64); parseErr == nil && value > after {
					after = value
				}
			}
			if strings.HasPrefix(line, "data: ") {
				fmt.Fprintln(output, strings.TrimPrefix(line, "data: "))
			}
		}
		scanErr := scanner.Err()
		response.Body.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if scanErr != nil {
			fmt.Fprintf(os.Stderr, "event stream interrupted; reconnecting after event %d\n", after)
		}
		if !waitForReconnect(ctx) {
			return ctx.Err()
		}
	}
}

func waitForReconnect(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
