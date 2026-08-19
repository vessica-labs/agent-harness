package worker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	RunID             string
	IssueKey          string
	IssueID           string
	ControlURL        string
	Capability        string
	LeaseOwner        string
	RepositoryID      string
	GitHubOwner       string
	GitHubRepo        string
	BaseBranch        string
	FeatureRequest    string
	CodexAuth         []byte
	CodexSlot         string
	CodexSessions     []CodexSession
	CodexParallelSafe bool
	Workspace         string
	Harnessctl        string
	CodexBinary       string
	CodexModel        string
	PlaywrightWorkers int
}

type CodexSession struct {
	ID   string `json:"id"`
	Auth []byte `json:"auth"`
}

func ConfigFromEnv() (Config, error) {
	feature, err := base64.StdEncoding.DecodeString(os.Getenv("HARNESS_FEATURE_REQUEST_B64"))
	if err != nil {
		return Config{}, errors.New("invalid HARNESS_FEATURE_REQUEST_B64")
	}
	auth, err := base64.StdEncoding.DecodeString(os.Getenv("HARNESS_CODEX_AUTH_B64"))
	if err != nil {
		return Config{}, errors.New("invalid HARNESS_CODEX_AUTH_B64")
	}
	var sessions []CodexSession
	if encoded := os.Getenv("HARNESS_CODEX_SESSIONS_B64"); encoded != "" {
		body, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil || json.Unmarshal(body, &sessions) != nil {
			return Config{}, errors.New("invalid HARNESS_CODEX_SESSIONS_B64")
		}
	}
	if len(sessions) == 0 && len(auth) > 0 {
		sessions = []CodexSession{{ID: os.Getenv("HARNESS_CODEX_AUTH_SLOT"), Auth: auth}}
	}
	config := Config{
		RunID: os.Getenv("HARNESS_RUN_ID"), IssueKey: os.Getenv("HARNESS_ISSUE_KEY"),
		IssueID: os.Getenv("HARNESS_ISSUE_ID"), ControlURL: strings.TrimRight(os.Getenv("HARNESS_CONTROL_URL"), "/"),
		Capability: os.Getenv("HARNESS_RUN_CAPABILITY"), LeaseOwner: os.Getenv("HARNESS_LEASE_OWNER"),
		RepositoryID: os.Getenv("HARNESS_REPOSITORY_ID"), GitHubOwner: os.Getenv("HARNESS_GITHUB_OWNER"),
		GitHubRepo: os.Getenv("HARNESS_GITHUB_REPO"), BaseBranch: os.Getenv("HARNESS_BASE_BRANCH"),
		FeatureRequest: string(feature), CodexAuth: auth, CodexSlot: os.Getenv("HARNESS_CODEX_AUTH_SLOT"),
		CodexSessions:     sessions,
		CodexParallelSafe: strings.EqualFold(os.Getenv("HARNESS_CODEX_PARALLEL_SAFE"), "true"),
		Workspace:         envDefault("HARNESS_WORKSPACE", "/workspace"),
		Harnessctl:        envDefault("HARNESS_HARNESSCTL", "/opt/agent-harness/harnessctl.py"),
		CodexBinary:       envDefault("HARNESS_CODEX_BINARY", "codex"),
		CodexModel:        envDefault("HARNESS_CODEX_MODEL", "gpt-5.3-codex"),
		PlaywrightWorkers: envPositiveInt("HARNESS_PLAYWRIGHT_WORKERS", 2),
	}
	for name, value := range map[string]string{"run id": config.RunID, "issue key": config.IssueKey,
		"control URL": config.ControlURL, "run capability": config.Capability,
		"GitHub owner": config.GitHubOwner, "GitHub repo": config.GitHubRepo, "base branch": config.BaseBranch,
		"Codex auth slot": config.CodexSlot} {
		if value == "" {
			return config, errors.New("missing worker " + name)
		}
	}
	config.Workspace = filepath.Clean(config.Workspace)
	return config, nil
}

func envPositiveInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
