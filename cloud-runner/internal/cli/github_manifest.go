package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
)

type manifestConversion struct {
	ID            int64  `json:"id"`
	PrivateKey    string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
}

func githubAppManifest(appName, callback, webhookURL string) []byte {
	manifest, _ := json.Marshal(map[string]any{
		"name": appName, "url": "https://github.com/vessica-labs/agent-harness",
		"description":  "Creates isolated Agent Harness branches and draft pull requests.",
		"redirect_url": callback, "public": false, "default_events": []string{"pull_request"},
		"hook_attributes":     map[string]any{"url": webhookURL, "active": true},
		"default_permissions": map[string]string{"metadata": "read", "contents": "write", "pull_requests": "write", "workflows": "write"},
	})
	return manifest
}

func githubManifestFlow(ctx context.Context, client *apiClient, owner, appName string) error {
	owner = strings.TrimSpace(owner)
	if owner != "@me" && !regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`).MatchString(owner) {
		return errors.New("GitHub manifest owner must be @me or a valid organization login")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	state, err := secure.GenerateKey()
	if err != nil {
		return err
	}
	callback := "http://" + listener.Addr().String() + "/callback"
	manifest := githubAppManifest(appName, callback, strings.TrimRight(client.url, "/")+"/webhooks/github")
	action := "https://github.com/settings/apps/new?state=" + state
	if owner != "@me" {
		action = "https://github.com/organizations/" + owner + "/settings/apps/new?state=" + state
	}
	type result struct {
		conversion manifestConversion
		err        error
	}
	completed := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = template.Must(template.New("manifest").Parse(`<!doctype html><title>Agent Harness GitHub App</title><main><h1>Create the Agent Harness GitHub App</h1><p>GitHub will show the exact four repository permissions before creation.</p><form method="post" action="{{.Action}}"><input type="hidden" name="manifest" value="{{.Manifest}}"><button type="submit">Continue to GitHub</button></form></main>`)).Execute(w, map[string]string{"Action": action, "Manifest": string(manifest)})
	})
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		if !secure.EqualSecret(r.URL.Query().Get("state"), state) {
			completed <- result{err: errors.New("GitHub manifest state mismatch")}
			http.Error(w, "Invalid state", http.StatusUnauthorized)
			return
		}
		conversion, conversionErr := convertGitHubManifest(r.Context(), r.URL.Query().Get("code"))
		completed <- result{conversion: conversion, err: conversionErr}
		if conversionErr != nil {
			http.Error(w, "GitHub App creation failed; return to the terminal.", http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, "GitHub App created. You may close this window and return to Codex.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)
	defer server.Shutdown(context.Background())
	startURL := "http://" + listener.Addr().String() + "/"
	if err := openBrowser(startURL); err != nil {
		fmt.Printf("Open %s to create the GitHub App.\n", startURL)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case value := <-completed:
		if value.err != nil {
			return value.err
		}
		credential, _ := json.Marshal(map[string]any{"app_id": value.conversion.ID, "private_key": value.conversion.PrivateKey,
			"webhook_secret": value.conversion.WebhookSecret})
		return putCredential(ctx, client, "github_app", string(credential))
	case <-time.After(10 * time.Minute):
		return errors.New("GitHub App manifest flow timed out")
	}
}

func convertGitHubManifest(ctx context.Context, code string) (manifestConversion, error) {
	if code == "" {
		return manifestConversion{}, errors.New("GitHub manifest callback omitted code")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/app-manifests/"+code+"/conversions", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return manifestConversion{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return manifestConversion{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return manifestConversion{}, fmt.Errorf("GitHub manifest conversion returned %d", response.StatusCode)
	}
	var value manifestConversion
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&value); err != nil {
		return value, err
	}
	if value.ID == 0 || value.PrivateKey == "" || value.WebhookSecret == "" {
		return value, errors.New("GitHub manifest conversion omitted app credentials or webhook secret")
	}
	return value, nil
}

func openBrowser(value string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	} else if runtime.GOOS == "windows" {
		command = "rundll32"
		return exec.Command(command, "url.dll,FileProtocolHandler", value).Start()
	}
	return exec.Command(command, value).Start()
}
