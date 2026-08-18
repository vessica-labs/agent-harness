package linearapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegistrationContextReturnsWorkspaceTeamsAndProjects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatal("missing Linear bearer token")
		}
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Query == "" {
			t.Fatal("missing GraphQL query")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"organization":{"id":"workspace-1","name":"Vessica"},"teams":{"nodes":[{"id":"team-1","name":"Engineering","key":"ENG"}]},"projects":{"nodes":[{"id":"project-1","name":"Agent Harness","teams":{"nodes":[{"id":"team-1"}]}}]}}}`))
	}))
	defer server.Close()

	client := New("access-token")
	client.endpoint = server.URL
	client.http = server.Client()
	value, err := client.RegistrationContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if value.Workspace.ID != "workspace-1" || len(value.Teams) != 1 || value.Teams[0].Key != "ENG" {
		t.Fatalf("unexpected Linear context: %+v", value)
	}
	if len(value.Projects) != 1 || len(value.Projects[0].TeamIDs) != 1 || value.Projects[0].TeamIDs[0] != "team-1" {
		t.Fatalf("unexpected Linear projects: %+v", value.Projects)
	}
}
