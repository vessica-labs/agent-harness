package worker

import "testing"

func TestConfigDefaultsToSupportedCodexModel(t *testing.T) {
	for key, value := range map[string]string{
		"HARNESS_RUN_ID":          "run-1",
		"HARNESS_ISSUE_KEY":       "AGE-1",
		"HARNESS_CONTROL_URL":     "https://control.example.test",
		"HARNESS_RUN_CAPABILITY":  "capability",
		"HARNESS_GITHUB_OWNER":    "vessica-labs",
		"HARNESS_GITHUB_REPO":     "agent-harness",
		"HARNESS_BASE_BRANCH":     "main",
		"HARNESS_CODEX_AUTH_SLOT": "codex-01",
	} {
		t.Setenv(key, value)
	}
	t.Setenv("HARNESS_CODEX_MODEL", "")

	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.CodexModel != "gpt-5.6-sol" {
		t.Fatalf("unexpected default Codex model: %q", config.CodexModel)
	}
}
