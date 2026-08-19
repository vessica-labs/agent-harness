package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

type authResponse struct {
	Member model.Member `json:"member"`
	Tokens tokenPair    `json:"tokens"`
}

func teamTestServer(t *testing.T) (*httptest.Server, *store.Memory) {
	t.Helper()
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	memory := store.NewMemory()
	server := New(Config{ManagementToken: "bootstrap-secret", MaxRequestBytes: 1 << 20, PublicURL: "https://runner.example"}, memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return httptest.NewServer(server.Handler()), memory
}

func callTeam(t *testing.T, client *http.Client, method, endpoint, bearer string, input any, output any) int {
	t.Helper()
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	request, _ := http.NewRequest(method, endpoint, bytes.NewReader(body))
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if output != nil {
		_ = json.NewDecoder(response.Body).Decode(output)
	}
	return response.StatusCode
}

func initializeOwner(t *testing.T, host *httptest.Server) authResponse {
	t.Helper()
	var result authResponse
	status := callTeam(t, host.Client(), http.MethodPost, host.URL+"/auth/v1/initialize", "bootstrap-secret", map[string]string{"display_name": "Owner", "device_name": "owner laptop"}, &result)
	if status != http.StatusCreated {
		t.Fatalf("initialize status %d", status)
	}
	return result
}

func TestTeamInviteIsSingleUseAndRolesAreEnforced(t *testing.T) {
	host, _ := teamTestServer(t)
	defer host.Close()
	owner := initializeOwner(t, host)
	if status := callTeam(t, host.Client(), http.MethodGet, host.URL+"/v1/status", "bootstrap-secret", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("bootstrap remained valid: %d", status)
	}
	var invite struct {
		JoinURL string `json:"join_url"`
	}
	if status := callTeam(t, host.Client(), http.MethodPost, host.URL+"/v1/team/invitations", owner.Tokens.AccessToken, map[string]any{"role": "viewer", "label": "Teammate", "expires_in_minutes": 60}, &invite); status != http.StatusCreated {
		t.Fatalf("invite status %d", status)
	}
	parsed, err := url.Parse(invite.JoinURL)
	if err != nil {
		t.Fatal(err)
	}
	fragment, _ := url.ParseQuery(parsed.Fragment)
	secret := fragment.Get("invite")
	if secret == "" {
		t.Fatal("join URL omitted fragment secret")
	}
	var teammate authResponse
	input := map[string]string{"invite_token": secret, "display_name": "Teammate", "device_name": "workstation"}
	if status := callTeam(t, host.Client(), http.MethodPost, host.URL+"/auth/v1/invitations/redeem", "", input, &teammate); status != http.StatusCreated {
		t.Fatalf("redeem status %d", status)
	}
	if teammate.Member.Role != "viewer" {
		t.Fatalf("unexpected role %q", teammate.Member.Role)
	}
	if status := callTeam(t, host.Client(), http.MethodPost, host.URL+"/auth/v1/invitations/redeem", "", input, nil); status != http.StatusGone {
		t.Fatalf("replay status %d", status)
	}
	if status := callTeam(t, host.Client(), http.MethodGet, host.URL+"/v1/runs", teammate.Tokens.AccessToken, nil, nil); status != http.StatusOK {
		t.Fatalf("viewer read status %d", status)
	}
	if status := callTeam(t, host.Client(), http.MethodPost, host.URL+"/v1/runs/unknown/resume", teammate.Tokens.AccessToken, map[string]any{}, nil); status != http.StatusForbidden {
		t.Fatalf("viewer mutation status %d", status)
	}
	if status := callTeam(t, host.Client(), http.MethodDelete, host.URL+"/v1/team/members/"+teammate.Member.ID, owner.Tokens.AccessToken, nil, nil); status != http.StatusOK {
		t.Fatalf("revoke member status %d", status)
	}
	if status := callTeam(t, host.Client(), http.MethodGet, host.URL+"/v1/runs", teammate.Tokens.AccessToken, nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("revoked access status %d", status)
	}
}

func TestRefreshRotationRevokesSessionOnReplay(t *testing.T) {
	host, _ := teamTestServer(t)
	defer host.Close()
	owner := initializeOwner(t, host)
	var rotated authResponse
	if status := callTeam(t, host.Client(), http.MethodPost, host.URL+"/auth/v1/token", "", map[string]string{"refresh_token": owner.Tokens.RefreshToken}, &rotated); status != http.StatusOK {
		t.Fatalf("refresh status %d", status)
	}
	if rotated.Tokens.RefreshToken == owner.Tokens.RefreshToken {
		t.Fatal("refresh token did not rotate")
	}
	if status := callTeam(t, host.Client(), http.MethodPost, host.URL+"/auth/v1/token", "", map[string]string{"refresh_token": owner.Tokens.RefreshToken}, nil); status != http.StatusUnauthorized {
		t.Fatalf("reuse status %d", status)
	}
	if status := callTeam(t, host.Client(), http.MethodGet, host.URL+"/v1/status", rotated.Tokens.AccessToken, nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("replayed session remained active: %d", status)
	}
}

func TestJoinPageKeepsInvitationOutOfRequestAndReferrers(t *testing.T) {
	host, _ := teamTestServer(t)
	defer host.Close()
	response, err := host.Client().Get(host.URL + "/join#invite=never-sent")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing no-referrer policy")
	}
	if strings.Contains(string(body), "never-sent") {
		t.Fatal("fragment secret reached server response")
	}
}
