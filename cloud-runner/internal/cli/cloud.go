package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

func runCloud(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("cloud requires profile, repo, runs, or auth")
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
	default:
		return fmt.Errorf("unknown cloud command %q", args[0])
	}
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
		return errors.New("repo requires add, list, or remove")
	}
	client, err := newAPI("")
	if err != nil {
		return err
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
	if args[0] != "add" {
		return errors.New("repo supports add, list, or remove")
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

func cloudRuns(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("runs requires list, show, watch, resume, cancel, or export")
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
	case "resume", "cancel":
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
		value, _ := json.Marshal(map[string]any{"app_id": *appID, "private_key": string(key)})
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
