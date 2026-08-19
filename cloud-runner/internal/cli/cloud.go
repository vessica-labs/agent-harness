package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

func runCloud(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("cloud requires profile, join, whoami, logout, team, repo, runs, or auth")
	}
	switch args[0] {
	case "profile":
		return cloudProfile(args[1:])
	case "repo":
		return cloudRepo(ctx, args[1:])
	case "runs":
		return cloudRuns(ctx, args[1:])
	case "auth":
		return cloudAuth(ctx, args[1:])
	case "team":
		return cloudTeam(ctx, args[1:])
	case "join":
		return cloudJoin(ctx, args[1:])
	case "whoami":
		return cloudWhoami(ctx)
	case "logout":
		return cloudLogout(ctx, args[1:])
	default:
		return fmt.Errorf("unknown cloud command %q", args[0])
	}
}

func defaultIdentity() (string, string) {
	name := strings.TrimSpace(os.Getenv("USER"))
	if name == "" {
		name = "Agent Harness user"
	}
	device, err := os.Hostname()
	if err != nil || device == "" {
		device = "Codex device"
	}
	return name, device
}

func cloudJoin(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cloud join", flag.ContinueOnError)
	profileName := flags.String("profile", "default", "local profile name")
	defaultName, defaultDevice := defaultIdentity()
	name := flags.String("name", defaultName, "team display name")
	device := flags.String("device", defaultDevice, "device name")
	inviteLink := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		inviteLink, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if inviteLink == "" && flags.NArg() == 1 {
		inviteLink = flags.Arg(0)
	}
	if inviteLink == "" || flags.NArg() > 0 {
		return errors.New("usage: agent-harness cloud join <invite-link> [--name NAME] [--device DEVICE]")
	}
	parsed, err := url.Parse(inviteLink)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("invite link must be a complete HTTPS URL")
	}
	if parsed.Scheme != "https" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" {
		return errors.New("invite link must use HTTPS")
	}
	token := parsed.Query().Get("invite")
	if token == "" {
		fragment, parseErr := url.ParseQuery(parsed.Fragment)
		if parseErr == nil {
			token = fragment.Get("invite")
		}
	}
	if token == "" {
		return errors.New("invite link does not contain an invitation secret")
	}
	base := parsed.Scheme + "://" + parsed.Host
	var result struct {
		Tokens sessionCredentials `json:"tokens"`
		Member model.Member       `json:"member"`
	}
	if err := publicAPI(ctx, base, http.MethodPost, "/auth/v1/invitations/redeem", "", map[string]string{"invite_token": token, "display_name": strings.TrimSpace(*name), "device_name": strings.TrimSpace(*device)}, &result); err != nil {
		return err
	}
	if err := saveSessionProfile(*profileName, base, result.Tokens); err != nil {
		return err
	}
	fmt.Printf("Joined %s as %s (%s). This device now has its own revocable session.\n", base, result.Member.DisplayName, result.Member.Role)
	return nil
}

func cloudTeam(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("team requires initialize, invite, members, sessions, audit, or revoke")
	}
	if args[0] == "initialize" {
		flags := flag.NewFlagSet("cloud team initialize", flag.ContinueOnError)
		profileName := flags.String("profile", "", "profile name")
		defaultName, defaultDevice := defaultIdentity()
		name := flags.String("name", defaultName, "owner display name")
		device := flags.String("device", defaultDevice, "device name")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		actual, urlValue, credentials, err := loadProfileSession(*profileName)
		if err != nil {
			return err
		}
		var result struct {
			Tokens sessionCredentials `json:"tokens"`
			Member model.Member       `json:"member"`
		}
		if err := publicAPI(ctx, urlValue, http.MethodPost, "/auth/v1/initialize", credentials.AccessToken, map[string]string{"display_name": strings.TrimSpace(*name), "device_name": strings.TrimSpace(*device)}, &result); err != nil {
			return err
		}
		if err := saveSessionProfile(actual, urlValue, result.Tokens); err != nil {
			return err
		}
		fmt.Printf("Team access initialized. %s is the owner; the shared bootstrap token is no longer accepted for ordinary API calls.\n", result.Member.DisplayName)
		return nil
	}
	client, err := newAPI("")
	if err != nil {
		return err
	}
	switch args[0] {
	case "invite":
		flags := flag.NewFlagSet("cloud team invite", flag.ContinueOnError)
		role := flags.String("role", "operator", "viewer, operator, or admin")
		label := flags.String("label", "", "recipient label")
		expiry := flags.Duration("expires", time.Hour, "one-time link lifetime")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		minutes := int(expiry.Minutes())
		if minutes < 1 {
			return errors.New("--expires must be at least one minute")
		}
		var result any
		if err := client.do(ctx, http.MethodPost, "/v1/team/invitations", map[string]any{"role": *role, "label": *label, "expires_in_minutes": minutes}, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "members":
		var result any
		if err := client.do(ctx, http.MethodGet, "/v1/team/members", nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "sessions":
		var result any
		if err := client.do(ctx, http.MethodGet, "/v1/team/sessions", nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "audit":
		var result any
		if err := client.do(ctx, http.MethodGet, "/v1/team/audit", nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "revoke":
		if len(args) != 3 {
			return errors.New("usage: cloud team revoke <member|session|invite> <id>")
		}
		var endpoint string
		switch args[1] {
		case "member":
			endpoint = "/v1/team/members/"
		case "session":
			endpoint = "/v1/team/sessions/"
		case "invite":
			endpoint = "/v1/team/invitations/"
		default:
			return errors.New("revoke target must be member, session, or invite")
		}
		var result any
		if err := client.do(ctx, http.MethodDelete, endpoint+url.PathEscape(args[2]), nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	default:
		return fmt.Errorf("unknown team command %q", args[0])
	}
}

func cloudWhoami(ctx context.Context) error {
	client, err := newAPI("")
	if err != nil {
		return err
	}
	var result any
	if err := client.do(ctx, http.MethodGet, "/v1/whoami", nil, &result); err != nil {
		return err
	}
	return printJSON(result)
}
func cloudLogout(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cloud logout", flag.ContinueOnError)
	profileName := flags.String("profile", "", "profile name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	client, err := newAPI(*profileName)
	if err != nil {
		return err
	}
	var result any
	if err := client.do(ctx, http.MethodPost, "/v1/logout", map[string]any{}, &result); err != nil {
		return err
	}
	name := client.profileName
	if err := deleteSecret(name); err != nil {
		return err
	}
	fmt.Println("This device has been logged out and its cloud session revoked.")
	return nil
}

func publicAPI(ctx context.Context, base, method, path, bearer string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("control plane returned %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func cloudProfile(args []string) error {
	if len(args) == 0 || args[0] != "set" {
		return errors.New("usage: agent-harness cloud profile set --url URL --token TOKEN")
	}
	flags := flag.NewFlagSet("cloud profile set", flag.ContinueOnError)
	name, url, token := flags.String("name", "default", "profile name"), flags.String("url", "", "control-plane URL"), flags.String("token", "", "management bearer token")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *url == "" || *token == "" {
		return errors.New("--url and --token are required")
	}
	return saveProfile(*name, *url, *token)
}

func cloudRepo(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("repo requires add, list, remove, discover-linear, or issue")
	}
	client, err := newAPI("")
	if err != nil {
		return err
	}
	if args[0] == "discover-linear" {
		var result any
		if err := client.do(ctx, http.MethodGet, "/v1/providers/linear/context", nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	}
	if args[0] == "list" {
		var result any
		if err := client.do(ctx, http.MethodGet, "/v1/repositories", nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	}
	if args[0] == "remove" {
		if len(args) != 2 {
			return errors.New("repo remove requires a repository id")
		}
		var result any
		if err := client.do(ctx, http.MethodDelete, "/v1/repositories/"+args[1], nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	}
	if args[0] == "issue" {
		return cloudRepoIssue(ctx, client, args[1:])
	}
	if args[0] != "add" {
		return errors.New("repo supports add, list, remove, discover-linear, or issue")
	}
	flags := flag.NewFlagSet("cloud repo add", flag.ContinueOnError)
	value := model.Repository{Enabled: true, TriggerLabel: "agent-harness", BaseBranch: "main"}
	flags.StringVar(&value.ID, "id", "", "stable repository id")
	flags.StringVar(&value.Name, "name", "", "display name")
	flags.StringVar(&value.GitHubOwner, "github-owner", "", "GitHub owner")
	flags.StringVar(&value.GitHubRepo, "github-repo", "", "GitHub repository")
	flags.Int64Var(&value.GitHubInstallation, "github-installation", 0, "GitHub App installation id")
	flags.StringVar(&value.BaseBranch, "base-branch", "main", "base branch")
	flags.StringVar(&value.LinearWorkspaceID, "linear-workspace", "", "Linear workspace/organization id")
	flags.StringVar(&value.LinearTeamID, "linear-team", "", "Linear team id")
	flags.StringVar(&value.LinearProjectID, "linear-project", "", "optional Linear project id")
	flags.StringVar(&value.TriggerLabel, "trigger-label", "agent-harness", "opt-in Linear label")
	flags.StringVar(&value.NotionParentID, "notion-parent", "", "Notion parent page id")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	var result any
	if err := client.do(ctx, http.MethodPost, "/v1/repositories", value, &result); err != nil {
		return err
	}
	return printJSON(result)
}

func cloudRepoIssue(ctx context.Context, client *apiClient, args []string) error {
	if len(args) == 0 {
		return errors.New("repo issue requires create or archive")
	}
	switch args[0] {
	case "create":
		flags := flag.NewFlagSet("cloud repo issue create", flag.ContinueOnError)
		repositoryID := flags.String("repo", "", "registered repository id")
		title := flags.String("title", "", "Linear issue title")
		descriptionFile := flags.String("description-file", "", "Markdown description file, or - for stdin")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *repositoryID == "" || strings.TrimSpace(*title) == "" || *descriptionFile == "" {
			return errors.New("repo issue create requires --repo, --title, and --description-file")
		}
		var body []byte
		var err error
		if *descriptionFile == "-" {
			body, err = io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		} else {
			body, err = os.ReadFile(*descriptionFile)
		}
		if err != nil {
			return err
		}
		description := strings.TrimSpace(string(body))
		if description == "" || len(description) >= 64<<10 {
			return errors.New("issue description must be between 1 byte and 64 KiB")
		}
		var result any
		endpoint := "/v1/repositories/" + url.PathEscape(*repositoryID) + "/linear/issues"
		if err := client.do(ctx, http.MethodPost, endpoint, map[string]string{"title": strings.TrimSpace(*title), "description": description}, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "archive":
		flags := flag.NewFlagSet("cloud repo issue archive", flag.ContinueOnError)
		repositoryID := flags.String("repo", "", "registered repository id")
		issueID := flags.String("issue", "", "Linear issue identifier or id")
		confirm := flags.Bool("yes", false, "confirm archival")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *repositoryID == "" || *issueID == "" || !*confirm {
			return errors.New("repo issue archive requires --repo, --issue, and --yes")
		}
		var result any
		endpoint := "/v1/repositories/" + url.PathEscape(*repositoryID) + "/linear/issues/" + url.PathEscape(*issueID) + "/archive"
		if err := client.do(ctx, http.MethodPost, endpoint, map[string]bool{"confirm": true}, &result); err != nil {
			return err
		}
		return printJSON(result)
	default:
		return fmt.Errorf("unknown repo issue command %q", args[0])
	}
}

func cloudRuns(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("runs requires list, show, watch, input, resume, cancel, reconcile, or export")
	}
	client, err := newAPI("")
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		var result any
		if err := client.do(ctx, http.MethodGet, "/v1/runs", nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "show":
		if len(args) != 2 {
			return errors.New("runs show requires a run id")
		}
		var result any
		if err := client.do(ctx, http.MethodGet, "/v1/runs/"+args[1], nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "watch":
		flags := flag.NewFlagSet("cloud runs watch", flag.ContinueOnError)
		runID, after := flags.String("run", "", "run id"), flags.Int64("after", 0, "event sequence cursor")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return client.watch(ctx, *runID, *after, os.Stdout)
	case "input":
		if len(args) < 2 {
			return errors.New("runs input requires a run id and --file")
		}
		flags := flag.NewFlagSet("cloud runs input", flag.ContinueOnError)
		file := flags.String("file", "", "clarified feature request file, or - for stdin")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if *file == "" {
			return errors.New("runs input requires --file")
		}
		var body []byte
		var err error
		if *file == "-" {
			body, err = io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		} else {
			body, err = os.ReadFile(*file)
		}
		if err != nil {
			return err
		}
		featureRequest := strings.TrimSpace(string(body))
		if featureRequest == "" {
			return errors.New("run input file is empty")
		}
		if len(featureRequest) >= 64<<10 {
			return errors.New("run input exceeds 64 KiB")
		}
		var result any
		if err := client.do(ctx, http.MethodPost, "/v1/runs/"+args[1]+"/input", map[string]string{"feature_request": featureRequest}, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "resume", "cancel", "reconcile":
		if len(args) != 2 {
			return fmt.Errorf("runs %s requires a run id", args[0])
		}
		var result any
		if err := client.do(ctx, http.MethodPost, "/v1/runs/"+args[1]+"/"+args[0], map[string]any{}, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "export":
		flags := flag.NewFlagSet("cloud runs export", flag.ContinueOnError)
		repo := flags.String("repo", ".", "target repository")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("runs export requires a run id")
		}
		runID := flags.Arg(0)
		temporary, err := os.CreateTemp("", "agent-harness-journal-*.tar.gz")
		if err != nil {
			return err
		}
		path := temporary.Name()
		temporary.Close()
		defer os.Remove(path)
		if err := client.download(ctx, "/v1/runs/"+runID+"/artifacts?path=journal/run.tar.gz", path); err != nil {
			return err
		}
		destination := filepath.Join(*repo, ".harness", "runs", runID)
		if _, err := os.Stat(destination); err == nil {
			return errors.New("target run directory already exists")
		}
		return extractExport(path, destination)
	default:
		return fmt.Errorf("unknown runs command %q", args[0])
	}
}

func cloudAuth(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("auth requires codex, github, linear, notion, or status")
	}
	client, err := newAPI("")
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		for _, name := range []string{"github_app", "linear_oauth", "linear_webhook_secret", "notion"} {
			var value any
			if err := client.do(ctx, http.MethodGet, "/v1/auth/"+name, nil, &value); err != nil {
				return err
			}
			printJSON(value)
		}
		var slots any
		if err := client.do(ctx, http.MethodGet, "/v1/auth-slots", nil, &slots); err != nil {
			return err
		}
		return printJSON(slots)
	case "codex":
		if len(args) < 2 || args[1] != "add" {
			return errors.New("usage: cloud auth codex add --slots 3")
		}
		flags := flag.NewFlagSet("cloud auth codex add", flag.ContinueOnError)
		slots := flags.Int("slots", 3, "independent Codex login sessions")
		verifyParallel := flags.Int("verify-parallel", 3, "simultaneous Codex processes used to verify one shared session")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		parallelSafe := true
		for index := 1; index <= *slots; index++ {
			home, err := os.MkdirTemp("", "agent-harness-codex-login-*")
			if err != nil {
				return err
			}
			command := exec.CommandContext(ctx, "codex", "login", "--device-auth")
			command.Env = append(os.Environ(), "CODEX_HOME="+home)
			command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := command.Run(); err != nil {
				os.RemoveAll(home)
				return err
			}
			if index == 1 && *verifyParallel > 1 {
				parallelSafe = verifyCodexParallel(ctx, home, *verifyParallel) == nil
			}
			auth, err := os.ReadFile(filepath.Join(home, "auth.json"))
			os.RemoveAll(home)
			if err != nil {
				return err
			}
			id := fmt.Sprintf("codex-%02d", index)
			var result any
			if err := client.do(ctx, http.MethodPost, "/v1/auth-slots", map[string]string{"id": id, "auth": string(auth)}, &result); err != nil {
				return err
			}
			printJSON(result)
		}
		if err := putCredential(ctx, client, "codex_parallel_safe", fmt.Sprint(parallelSafe)); err != nil {
			return err
		}
		if !parallelSafe {
			fmt.Fprintln(os.Stderr, "Codex session sharing was not safe; coder execution will serialize to one process per leased session.")
		}
		return nil
	case "github":
		if len(args) > 1 && args[1] == "upgrade-webhook" {
			if len(args) != 2 {
				return errors.New("usage: cloud auth github upgrade-webhook")
			}
			var result any
			if err := client.do(ctx, http.MethodPost, "/v1/auth/github/upgrade-webhook", map[string]any{}, &result); err != nil {
				return err
			}
			return printJSON(result)
		}
		flags := flag.NewFlagSet("cloud auth github", flag.ContinueOnError)
		appID, keyFile := flags.Int64("app-id", 0, "GitHub App id"), flags.String("private-key-file", "", "GitHub App private key PEM")
		manifestOwner := flags.String("manifest-owner", "", "GitHub organization; use @me for a personal app")
		appName := flags.String("name", "Agent Harness Control Plane", "GitHub App name for manifest flow")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *manifestOwner != "" {
			return githubManifestFlow(ctx, client, *manifestOwner, *appName)
		}
		if *appID == 0 || *keyFile == "" {
			return errors.New("use --manifest-owner OWNER, or provide --app-id and --private-key-file")
		}
		key, err := os.ReadFile(*keyFile)
		if err != nil {
			return err
		}
		webhookSecret := strings.TrimSpace(os.Getenv("GITHUB_WEBHOOK_SECRET"))
		if webhookSecret == "" {
			return errors.New("set GITHUB_WEBHOOK_SECRET to the GitHub App webhook secret for direct credential import")
		}
		value, _ := json.Marshal(map[string]any{"app_id": *appID, "private_key": string(key),
			"webhook_secret": webhookSecret})
		return putCredential(ctx, client, "github_app", string(value))
	case "linear":
		return linearAuth(ctx, client, args[1:])
	case "notion":
		token := os.Getenv("NOTION_TOKEN")
		if token == "" {
			return errors.New("set NOTION_TOKEN for this command")
		}
		return putCredential(ctx, client, "notion", token)
	default:
		return fmt.Errorf("unknown auth provider %q", args[0])
	}
}

func putCredential(ctx context.Context, client *apiClient, name, value string) error {
	var result any
	if err := client.do(ctx, http.MethodPut, "/v1/auth/"+name, map[string]string{"value": value}, &result); err != nil {
		return err
	}
	return printJSON(result)
}

func printJSON(value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		fmt.Println(string(body))
	}
	return err
}

func mustMarshal(value any) []byte { body, _ := json.Marshal(value); return body }

func verifyCodexParallel(ctx context.Context, home string, parallel int) error {
	var wait sync.WaitGroup
	errorsFound := make(chan error, parallel)
	for index := 0; index < parallel; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			command := exec.CommandContext(ctx, "codex", "exec", "--json", "--ignore-user-config", "--sandbox", "read-only", "-")
			command.Env = append(os.Environ(), "CODEX_HOME="+home)
			command.Stdin = strings.NewReader("Reply with the single word OK. Do not use tools.")
			command.Stdout, command.Stderr = io.Discard, io.Discard
			if err := command.Run(); err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		return err
	}
	return nil
}
