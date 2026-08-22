package cli

import (
	"encoding/json"
	"testing"
)

func TestGitHubAppManifestRequestsWorkflowWrites(t *testing.T) {
	var manifest struct {
		DefaultPermissions map[string]string `json:"default_permissions"`
	}
	if err := json.Unmarshal(githubAppManifest("Vessica", "http://127.0.0.1/callback", "https://runner.example/webhooks/github"), &manifest); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"metadata": "read", "contents": "write", "pull_requests": "write", "workflows": "write"}
	if len(manifest.DefaultPermissions) != len(want) {
		t.Fatalf("permissions = %#v, want %#v", manifest.DefaultPermissions, want)
	}
	for permission, access := range want {
		if manifest.DefaultPermissions[permission] != access {
			t.Fatalf("permission %q = %q, want %q", permission, manifest.DefaultPermissions[permission], access)
		}
	}
}
