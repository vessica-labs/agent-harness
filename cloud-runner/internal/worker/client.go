package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

type controlClient struct {
	baseURL string
	token   string
	runID   string
	http    *http.Client
}

func newControlClient(config Config) *controlClient {
	return &controlClient{baseURL: config.ControlURL, token: config.Capability, runID: config.RunID,
		http: &http.Client{Timeout: 2 * time.Minute}}
}

func (c *controlClient) request(ctx context.Context, method, action, mediaType string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/internal/v1/runs/"+c.runID+"/"+action, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if mediaType != "" {
		request.Header.Set("Content-Type", mediaType)
	}
	return c.http.Do(request)
}

func (c *controlClient) event(ctx context.Context, event model.Event) error {
	body, _ := json.Marshal(event)
	response, err := c.request(ctx, http.MethodPost, "events", "application/json", bytes.NewReader(body))
	return drain(response, err, http.StatusCreated)
}

func (c *controlClient) heartbeat(ctx context.Context, owner string) error {
	body, _ := json.Marshal(map[string]string{"owner": owner})
	response, err := c.request(ctx, http.MethodPost, "heartbeat", "application/json", bytes.NewReader(body))
	return drain(response, err, http.StatusOK)
}

func (c *controlClient) uploadJournal(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	response, err := c.request(ctx, http.MethodPut, "journal", "application/gzip", file)
	return drain(response, err, http.StatusOK)
}

func (c *controlClient) downloadJournal(ctx context.Context, destination string) (bool, error) {
	response, err := c.request(ctx, http.MethodGet, "journal", "", nil)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, responseError(response)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	_, copyErr := io.Copy(file, io.LimitReader(response.Body, 101<<20))
	closeErr := file.Close()
	if copyErr != nil {
		return false, copyErr
	}
	return true, closeErr
}

func (c *controlClient) githubToken(ctx context.Context) (string, error) {
	response, err := c.request(ctx, http.MethodPost, "github-token", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", responseError(response)
	}
	var value struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		return "", err
	}
	if value.Token == "" {
		return "", errors.New("control plane returned an empty GitHub token")
	}
	return value.Token, nil
}

func (c *controlClient) returnAuth(ctx context.Context, slotID string, auth []byte, authError string) error {
	body, _ := json.Marshal(map[string]string{"slot_id": slotID, "auth": string(auth), "error": authError})
	response, err := c.request(ctx, http.MethodPost, "auth-return", "application/json", bytes.NewReader(body))
	return drain(response, err, http.StatusOK)
}

func (c *controlClient) sync(ctx context.Context, input any, output any) error {
	body, _ := json.Marshal(input)
	response, err := c.request(ctx, http.MethodPost, "sync", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError(response)
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func drain(response *http.Response, err error, expected int) error {
	if err != nil {
		return err
	}
	if response == nil {
		return errors.New("empty control-plane response")
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		return responseError(response)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return nil
}

func responseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return fmt.Errorf("control plane returned status %d: %s", response.StatusCode, bytes.TrimSpace(body))
}
