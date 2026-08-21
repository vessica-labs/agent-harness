package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
)

func runRailway(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("railway requires init, deploy, status, logs, or upgrade")
	}
	switch args[0] {
	case "init":
		return railwayInit(ctx, args[1:])
	case "deploy":
		return railwayDeploy(ctx, args[1:])
	case "status":
		return railwayPassthrough(ctx, "status", args[1:]...)
	case "logs":
		return railwayPassthrough(ctx, "logs", args[1:]...)
	case "upgrade":
		return railwayUpgrade(ctx, args[1:])
	default:
		return fmt.Errorf("unknown railway command %q", args[0])
	}
}

func railwayInit(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("railway init", flag.ContinueOnError)
	project := flags.String("project", "", "Railway project id")
	environment := flags.String("environment", "production", "Railway environment")
	service := flags.String("service", "control-plane", "control-plane service")
	publicURL := flags.String("url", "", "public control-plane URL")
	checkpoint := flags.String("checkpoint", "agent-harness-worker-v1", "sandbox checkpoint")
	postgresService := flags.String("postgres-service", "Postgres", "Railway Postgres service name")
	previewService := flags.String("preview-service", "preview-edge", "public preview edge service")
	previewURL := flags.String("preview-url", "", "public HTTPS preview edge URL; empty disables previews")
	profileName := flags.String("profile", "default", "local cloud profile name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	workspaceToken := os.Getenv("RAILWAY_API_TOKEN")
	if *project == "" || *publicURL == "" || workspaceToken == "" {
		return errors.New("--project, --url, and RAILWAY_API_TOKEN are required")
	}
	if !strings.HasPrefix(strings.ToLower(*publicURL), "https://") {
		return errors.New("--url must be the public HTTPS control-plane URL")
	}
	if *previewURL != "" && !strings.HasPrefix(strings.ToLower(*previewURL), "https://") {
		return errors.New("--preview-url must be the public HTTPS preview edge URL")
	}
	if err := verifyRailwaySandboxAccess(ctx, *project, *environment); err != nil {
		return err
	}
	managementToken, err := secure.GenerateKey()
	if err != nil {
		return err
	}
	credentialKey, err := secure.GenerateKey()
	if err != nil {
		return err
	}
	common := []string{"--project", *project, "--environment", *environment, "--service", *service, "--skip-deploys"}
	secrets := map[string]string{
		"HARNESS_MANAGEMENT_TOKEN": managementToken,
		"HARNESS_CREDENTIAL_KEY":   credentialKey,
		"RAILWAY_API_TOKEN":        workspaceToken,
	}
	for key, value := range secrets {
		commandArgs := append([]string{"variable", "set", key, "--stdin"}, common...)
		if err := railwayCommand(ctx, strings.NewReader(value), io.Discard, commandArgs...); err != nil {
			return err
		}
	}
	nonSecret := []string{
		"HARNESS_PUBLIC_URL=" + strings.TrimRight(*publicURL, "/"),
		"HARNESS_RAILWAY_PROJECT=" + *project,
		"HARNESS_RAILWAY_ENVIRONMENT=" + *environment,
		"HARNESS_SANDBOX_CHECKPOINT=" + *checkpoint,
		"HARNESS_MAX_ACTIVE_RUNS=3",
		"DATABASE_URL=${{" + *postgresService + ".DATABASE_URL}}",
	}
	commandArgs := append([]string{"variable", "set"}, nonSecret...)
	commandArgs = append(commandArgs, common...)
	if err := railwayCommand(ctx, nil, io.Discard, commandArgs...); err != nil {
		return err
	}
	if *previewURL != "" {
		edgeToken, err := secure.GenerateKey()
		if err != nil {
			return err
		}
		setSecret := func(service, key, value string) error {
			serviceCommon := []string{"--project", *project, "--environment", *environment, "--service", service, "--skip-deploys"}
			commandArgs := append([]string{"variable", "set", key, "--stdin"}, serviceCommon...)
			return railwayCommand(ctx, strings.NewReader(value), io.Discard, commandArgs...)
		}
		if err := setSecret(*service, "HARNESS_PREVIEW_EDGE_TOKEN", edgeToken); err != nil {
			return err
		}
		if err := setSecret(*previewService, "HARNESS_PREVIEW_EDGE_TOKEN", edgeToken); err != nil {
			return err
		}
		controlVariables := append([]string{"variable", "set",
			"HARNESS_PREVIEW_PUBLIC_URL=" + strings.TrimRight(*previewURL, "/")}, common...)
		if err := railwayCommand(ctx, nil, io.Discard, controlVariables...); err != nil {
			return err
		}
		edgeVariables := []string{"variable", "set",
			"HARNESS_SERVICE_ROLE=preview-edge",
			"HARNESS_PREVIEW_UPSTREAM=http://${{" + *service + ".RAILWAY_PRIVATE_DOMAIN}}:${{" + *service + ".PORT}}",
			"--project", *project, "--environment", *environment, "--service", *previewService, "--skip-deploys"}
		if err := railwayCommand(ctx, nil, io.Discard, edgeVariables...); err != nil {
			return err
		}
	}
	if err := saveProfile(*profileName, *publicURL, managementToken); err != nil {
		return err
	}
	fmt.Printf("Configured Railway service and bootstrap profile %q. After the first successful deployment, run: agent-harness cloud team initialize\n", *profileName)
	return nil
}

func railwayUpgrade(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("railway upgrade", flag.ContinueOnError)
	project := flags.String("project", "", "Railway project id")
	environment := flags.String("environment", "production", "Railway environment")
	version := flags.String("version", "", "published GitHub release tag, for example v0.1.0")
	checkpoint := flags.String("checkpoint", "", "checkpoint name; defaults to agent-harness-worker-<version>")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *project == "" || !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`).MatchString(*version) {
		return errors.New("--project and a release --version such as v0.1.0 are required")
	}
	if *checkpoint == "" {
		*checkpoint = "agent-harness-worker-" + strings.TrimPrefix(*version, "v")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(*checkpoint) {
		return errors.New("checkpoint name may contain only letters, numbers, dots, underscores, and hyphens")
	}
	if err := verifyRailwaySandboxAccess(ctx, *project, *environment); err != nil {
		return err
	}
	template := *checkpoint + "-template"
	commands := railwayUpgradeCommands(*version)
	buildArgs := []string{"sandbox", "template", "build", "--name", template, "--wait", "--json", "--project", *project, "--environment", *environment}
	for _, command := range commands {
		buildArgs = append(buildArgs, "--command", command)
	}
	if err := railwayCommand(ctx, nil, io.Discard, buildArgs...); err != nil {
		return fmt.Errorf("build worker template: %w", err)
	}
	var created bytes.Buffer
	if err := railwayCommand(ctx, nil, &created, "sandbox", "create", "--template", template, "--idle-timeout-minutes", "30", "--json",
		"--project", *project, "--environment", *environment); err != nil {
		return fmt.Errorf("create checkpoint source sandbox: %w", err)
	}
	var document any
	if err := json.Unmarshal(created.Bytes(), &document); err != nil {
		return fmt.Errorf("decode source sandbox: %w", err)
	}
	id := findJSONField(document, "id", "sandboxId", "sandbox_id")
	if id == "" {
		return errors.New("Railway did not return the source sandbox id")
	}
	defer railwayCommand(context.WithoutCancel(ctx), nil, io.Discard, "sandbox", "destroy", "--id", id, "--project", *project, "--environment", *environment)
	if err := waitForRailwaySandbox(ctx, *project, *environment, id); err != nil {
		return fmt.Errorf("wait for checkpoint source sandbox: %w", err)
	}
	if err := railwayCommand(ctx, nil, os.Stdout, "sandbox", "checkpoint", "create", *checkpoint, "--id", id, "--json",
		"--project", *project, "--environment", *environment); err != nil {
		return fmt.Errorf("capture worker checkpoint: %w", err)
	}
	fmt.Printf("Worker checkpoint %q is ready. Set HARNESS_SANDBOX_CHECKPOINT=%s on the control plane.\n", *checkpoint, *checkpoint)
	return nil
}

func railwayUpgradeCommands(version string) []string {
	base := "https://github.com/vessica-labs/agent-harness/releases/download/" + version
	return []string{
		// Railway's sandbox base already includes gh. Reinstalling it can fail when dpkg
		// tries to create a backup hard link across overlay filesystem boundaries.
		"apt-get update && apt-get install -y --no-install-recommends bash build-essential ca-certificates chromium curl git jq make openssh-client procps python3 ripgrep",
		"curl -fsSL https://deb.nodesource.com/setup_22.x -o /tmp/nodesource.sh && bash /tmp/nodesource.sh && apt-get install -y --no-install-recommends nodejs && rm -rf /var/lib/apt/lists/* /tmp/nodesource.sh",
		"curl -fsSL " + base + "/agent-harness-linux-amd64 -o /usr/local/bin/agent-harness && chmod 0755 /usr/local/bin/agent-harness",
		"mkdir -p /opt/agent-harness && curl -fsSL " + base + "/harnessctl.py -o /opt/agent-harness/harnessctl.py && chmod 0755 /opt/agent-harness/harnessctl.py",
		"npm install --global @openai/codex@0.144.1",
		"mkdir -p /opt/agent-harness/bin && cp /usr/local/bin/agent-harness /opt/agent-harness/bin/agent-harness && sha256sum /opt/agent-harness/bin/agent-harness | awk '{print $1}' >/opt/agent-harness/bin/agent-harness.sha256",
		"printf '%s\\n' '{\"schema\":1,\"kind\":\"agent-harness-toolchain\",\"codex\":\"0.144.1\",\"node\":\"22\"}' >/opt/agent-harness/runtime-manifest.json",
	}
}

func waitForRailwaySandbox(ctx context.Context, project, environment, id string) error {
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		var listed bytes.Buffer
		if err := railwayCommand(ctx, nil, &listed, "sandbox", "list", "--json", "--project", project, "--environment", environment); err != nil {
			return err
		}
		var document any
		if err := json.Unmarshal(listed.Bytes(), &document); err != nil {
			return fmt.Errorf("decode sandbox status: %w", err)
		}
		state := strings.ToUpper(findSandboxState(document, id))
		switch state {
		case "RUNNING":
			return nil
		case "FAILED", "STOPPED", "REMOVED", "DESTROYED":
			return fmt.Errorf("sandbox %s entered terminal state %s", id, state)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("sandbox %s did not become running within two minutes (last state %q)", id, state)
		case <-ticker.C:
		}
	}
}

func findSandboxState(value any, id string) string {
	switch current := value.(type) {
	case map[string]any:
		if directJSONField(current, "id", "sandboxId", "sandbox_id") == id {
			return directJSONField(current, "state", "status")
		}
		for _, child := range current {
			if found := findSandboxState(child, id); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range current {
			if found := findSandboxState(child, id); found != "" {
				return found
			}
		}
	}
	return ""
}

func directJSONField(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if found, ok := value[key]; ok && found != nil {
			return fmt.Sprint(found)
		}
	}
	return ""
}

func verifyRailwaySandboxAccess(ctx context.Context, project, environment string) error {
	if err := railwayCommand(ctx, nil, io.Discard, "sandbox", "list", "--json", "--project", project, "--environment", environment); err != nil {
		return fmt.Errorf("Railway Sandboxes are unavailable for this project; enable Sandboxes/Priority Boarding and retry: %w", err)
	}
	return nil
}

func findJSONField(value any, keys ...string) string {
	switch current := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if found, ok := current[key]; ok && found != nil {
				return fmt.Sprint(found)
			}
		}
		for _, child := range current {
			if found := findJSONField(child, keys...); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range current {
			if found := findJSONField(child, keys...); found != "" {
				return found
			}
		}
	}
	return ""
}

func railwayDeploy(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("railway deploy", flag.ContinueOnError)
	project := flags.String("project", "", "Railway project id")
	environment := flags.String("environment", "production", "Railway environment")
	service := flags.String("service", "control-plane", "control-plane service")
	path := flags.String("path", "", "cloud-runner directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errors.New("--project is required")
	}
	if *path == "" {
		working, _ := os.Getwd()
		if filepath.Base(working) == "cloud-runner" {
			*path = working
		} else {
			*path = filepath.Join(working, "cloud-runner")
		}
	}
	return railwayCommand(ctx, nil, os.Stdout, "up", "--detach", "--json", "--project", *project,
		"--environment", *environment, "--service", *service, "--path-as-root", *path)
}

func railwayPassthrough(ctx context.Context, command string, args ...string) error {
	return railwayCommand(ctx, os.Stdin, os.Stdout, append([]string{command}, args...)...)
}

func railwayCommand(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "railway", args...)
	command.Env = append(os.Environ(), "RAILWAY_CALLER=agent-harness-cli", "RAILWAY_AGENT_SESSION=agent-harness-cli")
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, os.Stderr
	return command.Run()
}
