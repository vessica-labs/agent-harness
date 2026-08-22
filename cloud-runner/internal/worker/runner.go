package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

// Railway Sandboxes place a conservative safe-git wrapper in /usr/local/bin.
// Codex subprocesses should keep that guardrail, while the deterministic
// orchestrator needs the distro Git binary for controlled worktree lifecycle
// operations that the wrapper intentionally denies.
const orchestratorGit = "/usr/bin/git"

type Runner struct {
	config          Config
	client          *controlClient
	logger          *slog.Logger
	repo            string
	runDir          string
	codexHome       string
	codexSessions   []runtimeCodexSession
	pipeline        Pipeline
	localLease      string
	githubToken     string
	branchPublished bool
	deliveryBranch  string
}

type runtimeCodexSession struct {
	id   string
	home string
	auth []byte
}

type inputRequestSignal struct {
	request model.InputRequest
}

func (e *inputRequestSignal) Error() string { return "stage requested human input" }

type inputPolicyError struct{ message string }

func (e *inputPolicyError) Error() string { return e.message }

func New(config Config, logger *slog.Logger) *Runner {
	return &Runner{config: config, client: newControlClient(config), logger: logger}
}

func (r *Runner) Run(ctx context.Context) (runErr error) {
	startupStarted := time.Now()
	startupPhase := "worker_process"
	pipelineReady := false
	defer func() {
		if runErr != nil && !pipelineReady {
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_ = r.event(failureCtx, "run.failed", "error", runErr.Error(), "", map[string]any{
				"phase": startupPhase, "startup_duration_ms": time.Since(startupStarted).Milliseconds(),
			})
		}
	}()
	_ = r.event(ctx, "worker.starting", "info", "Sandbox worker process started", "", map[string]any{
		"checkpoint": os.Getenv("HARNESS_SANDBOX_CHECKPOINT"),
	})
	phaseStarted := time.Now()
	if err := r.prepareFilesystem(); err != nil {
		return err
	}
	r.startupTiming(ctx, "filesystem_prepare", phaseStarted, map[string]any{"status": "completed"})
	r.remoteStartupTimings(ctx)
	defer func() {
		authError := ""
		if runErr != nil && strings.Contains(strings.ToLower(runErr.Error()), "auth") {
			authError = runErr.Error()
		}
		returnCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, session := range r.codexSessions {
			auth, err := os.ReadFile(filepath.Join(session.home, "auth.json"))
			if err != nil {
				auth = session.auth
			}
			if err := r.client.returnAuth(returnCtx, session.id, auth, authError); err != nil {
				r.logger.Error("return Codex auth slot", "slot", session.id, "error", err)
			}
		}
	}()
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go r.heartbeat(heartbeatCtx)

	var err error
	startupPhase, phaseStarted = "github_credential", time.Now()
	r.githubToken, err = r.client.githubToken(ctx)
	if err != nil {
		return fmt.Errorf("obtain repository credential: %w", err)
	}
	r.startupTiming(ctx, startupPhase, phaseStarted, map[string]any{"status": "completed"})
	startupPhase, phaseStarted = "repository_checkout", time.Now()
	_, repositoryCached := os.Stat(filepath.Join(r.repo, ".git"))
	if err := r.checkout(ctx); err != nil {
		return err
	}
	r.startupTiming(ctx, startupPhase, phaseStarted, map[string]any{"status": "completed", "cache_hit": repositoryCached == nil})
	startupPhase, phaseStarted = "pipeline_load", time.Now()
	pipelinePath := filepath.Join(r.repo, ".harness", "pipeline.yaml")
	r.pipeline, err = loadPipeline(pipelinePath)
	if err != nil {
		return fmt.Errorf("load pipeline: %w", err)
	}
	r.startupTiming(ctx, startupPhase, phaseStarted, map[string]any{"status": "completed"})
	r.runDir = runDirectory(r.repo, r.pipeline, r.config.RunID)
	startupPhase, phaseStarted = "journal_restore", time.Now()
	if err := r.restoreOrInitialize(ctx, pipelinePath); err != nil {
		return err
	}
	r.startupTiming(ctx, startupPhase, phaseStarted, map[string]any{"status": "completed"})
	if _, err := r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json",
		string(mustJSON(map[string]any{"git": map[string]any{"branch": r.branchName(), "base": r.config.BaseBranch}})), "--event", "git.branch-prepared"); err != nil {
		return err
	}

	for order, stage := range r.pipeline.Stages {
		if err := r.event(ctx, "pipeline.stage", "info", "Pipeline stage registered", stage.ID,
			map[string]any{"order": order, "needs": stage.Needs, "mode": stage.Mode, "parallelism": stage.Parallelism, "agent": stage.Agent}); err != nil {
			return err
		}
	}
	if err := r.event(ctx, "run.started", "info", "Sandbox worker began pipeline execution", "", nil); err != nil {
		return err
	}
	pipelineReady = true
	r.startupTiming(ctx, "sandbox_to_pipeline_ready", startupStarted, map[string]any{"status": "completed"})
	if err := r.checkpoint(ctx); err != nil {
		return err
	}

	repairs, err := r.loadRepairCounts()
	if err != nil {
		return err
	}
	for index := 0; index < len(r.pipeline.Stages); index++ {
		stage := r.pipeline.Stages[index]
		completed, err := r.stageCompleted(stage.ID)
		if err != nil {
			return err
		}
		if completed {
			continue
		}
		if err := r.requireDependencies(stage); err != nil {
			return r.pause(ctx, stage, err)
		}
		if err := r.executeStageWithRetries(ctx, stage); err != nil {
			var input *inputRequestSignal
			if errors.As(err, &input) {
				return r.awaitInput(ctx, stage, input.request)
			}
			var request *repairRequest
			if errors.As(err, &request) {
				target, repairErr := r.handleRepair(ctx, stage, request, repairs)
				if repairErr != nil {
					return r.pause(ctx, stage, repairErr)
				}
				index = target - 1
				continue
			}
			return r.pause(ctx, stage, err)
		}
	}
	if r.localLease != "" {
		_, _ = r.harness(ctx, r.repo, "release-lease", "--repo", r.repo, "--issue-key", r.config.IssueKey, "--session-token", r.localLease)
	}
	if err := r.checkpoint(ctx); err != nil {
		return err
	}
	r.startPreview(ctx)
	return r.event(ctx, "run.completed", "info", "Pipeline completed and produced a draft pull request", "", nil)
}

func (r *Runner) executeStageWithRetries(ctx context.Context, stage Stage) error {
	if recovered, err := r.recoverRepairRequest(stage); err != nil {
		return err
	} else if recovered != nil {
		return recovered
	}
	const maxAttempts = 3
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		last = r.executeStage(ctx, stage)
		if last == nil {
			return nil
		}
		var repair *repairRequest
		var input *inputRequestSignal
		var policy *inputPolicyError
		if errors.As(last, &repair) || errors.As(last, &input) || errors.As(last, &policy) || ctx.Err() != nil {
			return last
		}
		if attempt == maxAttempts {
			break
		}
		_ = r.event(context.WithoutCancel(ctx), "stage.retrying", "warning", "Stage attempt failed; retrying from the durable journal", stage.ID,
			map[string]any{"attempt": attempt, "max_attempts": maxAttempts, "error": last.Error()})
		_, _ = r.harness(context.WithoutCancel(ctx), r.repo, "set-stage", "--run-dir", r.runDir, "--stage", stage.ID,
			"--status", "pending", "--details-json", string(mustJSON(map[string]any{"retry_attempt": attempt + 1, "last_error": last.Error()})))
		checkpointCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		_ = r.checkpoint(checkpointCtx)
		cancel()
	}
	return fmt.Errorf("stage %s failed after %d attempts: %w", stage.ID, maxAttempts, last)
}

func (r *Runner) prepareFilesystem() error {
	if err := os.MkdirAll(r.config.Workspace, 0o700); err != nil {
		return err
	}
	r.repo = filepath.Join(r.config.Workspace, "repo")
	if len(r.config.CodexSessions) == 0 {
		return errors.New("Codex authentication slot is empty")
	}
	if len(r.config.CodexSessions) != 1 {
		return errors.New("a worker must receive exactly one top-level Codex authentication slot")
	}
	for index, configured := range r.config.CodexSessions {
		if configured.ID == "" || len(configured.Auth) == 0 {
			return errors.New("Codex authentication session is incomplete")
		}
		home := filepath.Join(r.config.Workspace, fmt.Sprintf("codex-home-%02d", index+1))
		if err := os.MkdirAll(home, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(home, "auth.json"), configured.Auth, 0o600); err != nil {
			return err
		}
		session := runtimeCodexSession{id: configured.ID, home: home, auth: configured.Auth}
		r.codexSessions = append(r.codexSessions, session)
	}
	r.codexHome = r.codexSessions[0].home
	return nil
}

func (r *Runner) checkout(ctx context.Context) error {
	branch := r.branchName()
	_, repositoryErr := os.Stat(filepath.Join(r.repo, ".git"))
	repositoryCached := repositoryErr == nil
	if errors.Is(repositoryErr, os.ErrNotExist) {
		if err := os.RemoveAll(r.repo); err != nil {
			return err
		}
		url := fmt.Sprintf("https://github.com/%s/%s.git", r.config.GitHubOwner, r.config.GitHubRepo)
		if _, err := r.gitWithPublicFallback(ctx, r.config.Workspace, "clone", "--origin", "origin", url, r.repo); err != nil {
			return fmt.Errorf("clone repository: %w", err)
		}
	}
	if _, err := r.gitWithPublicFallback(ctx, r.repo, "fetch", "origin", "--prune"); err != nil {
		if !repositoryCached {
			return err
		}
		if _, verifyErr := runCommand(ctx, r.repo, sanitizedEnvironment(""), orchestratorGit, "rev-parse", "--verify", "origin/"+r.config.BaseBranch); verifyErr != nil {
			return err
		}
		_ = r.event(context.WithoutCancel(ctx), "run.infrastructure.stage", "warning", "Remote sync failed; using the verified repository checkpoint", "", map[string]any{
			"stage": "repository_sync", "status": "degraded", "cache_hit": true, "mode": "checkpoint_fallback",
		})
	}
	_, remoteErr := r.gitWithPublicFallback(ctx, r.repo, "ls-remote", "--exit-code", "--heads", "origin", branch)
	if remoteErr == nil {
		r.branchPublished = true
		_, err := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), orchestratorGit, "checkout", "-B", branch, "origin/"+branch)
		return err
	}
	_, err := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), orchestratorGit, "checkout", "-B", branch, "origin/"+r.config.BaseBranch)
	return err
}

func (r *Runner) gitWithPublicFallback(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	output, err := runCommand(ctx, cwd, gitEnvironment(r.githubToken), orchestratorGit, args...)
	if err == nil || r.githubToken == "" {
		return output, err
	}
	fallback, fallbackErr := runCommand(ctx, cwd, sanitizedEnvironment(""), orchestratorGit, args...)
	if fallbackErr == nil {
		_ = r.event(context.WithoutCancel(ctx), "run.infrastructure.stage", "warning", "Authenticated Git failed; public repository access succeeded", "", map[string]any{
			"stage": "repository_auth_fallback", "status": "completed",
		})
		return fallback, nil
	}
	return output, err
}

func (r *Runner) startupTiming(ctx context.Context, stage string, started time.Time, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["stage"] = stage
	payload["duration_ms"] = time.Since(started).Milliseconds()
	_ = r.event(ctx, "run.infrastructure.stage", "info", "Startup phase completed", "", payload)
}

func (r *Runner) remoteStartupTimings(ctx context.Context) {
	parse := func(key string) int64 {
		value, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
		return value
	}
	requested := parse("HARNESS_SANDBOX_REQUESTED_AT_MS")
	bootstrap := parse("HARNESS_BOOTSTRAP_STARTED_AT_MS")
	downloadStarted := parse("HARNESS_WORKER_DOWNLOAD_STARTED_AT_MS")
	downloaded := parse("HARNESS_WORKER_DOWNLOADED_AT_MS")
	for _, item := range []struct {
		stage      string
		start, end int64
		cacheHit   bool
	}{
		{stage: "checkpoint_boot", start: requested, end: bootstrap},
		{stage: "worker_download", start: downloadStarted, end: downloaded, cacheHit: os.Getenv("HARNESS_WORKER_CACHE_HIT") == "1"},
	} {
		if item.start <= 0 || item.end < item.start {
			continue
		}
		payload := map[string]any{"stage": item.stage, "duration_ms": item.end - item.start, "status": "completed"}
		if item.stage == "worker_download" {
			payload["cache_hit"] = item.cacheHit
		}
		_ = r.event(ctx, "run.infrastructure.stage", "info", "Startup phase completed", "", payload)
	}
}

func (r *Runner) restoreOrInitialize(ctx context.Context, pipelinePath string) error {
	archive := filepath.Join(r.config.Workspace, "journal.tar.gz")
	restored, err := r.client.downloadJournal(ctx, archive)
	if err != nil {
		return fmt.Errorf("download run journal: %w", err)
	}
	if restored {
		if err := os.MkdirAll(r.runDir, 0o700); err != nil {
			return err
		}
		if err := extractDirectory(archive, r.runDir); err != nil {
			return fmt.Errorf("restore run journal: %w", err)
		}
	}
	if _, err := r.harness(ctx, r.repo, "validate-config", filepath.Join(r.repo, ".harness", "config.yaml")); err != nil {
		return err
	}
	if _, err := r.harness(ctx, r.repo, "validate-pipeline", pipelinePath, "--repo", r.repo); err != nil {
		return err
	}
	output, err := r.harness(ctx, r.repo, "init-run", "--repo", r.repo, "--provider", "linear",
		"--issue-key", r.config.IssueKey, "--stages", "full", "--run-id", r.config.RunID, "--reclaim-lease")
	if err != nil {
		return err
	}
	var initialized struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(output, &initialized); err != nil {
		return fmt.Errorf("decode harness init result: %w", err)
	}
	r.localLease = initialized.SessionToken
	productComplete, err := r.stageCompleted("product")
	if err != nil {
		return err
	}
	if !productComplete {
		temporary := filepath.Join(r.config.Workspace, "feature-request.md")
		if err := os.WriteFile(temporary, []byte(r.config.FeatureRequest), 0o600); err != nil {
			return err
		}
		if _, err := r.harness(ctx, r.repo, "materialize-source", "--pipeline", pipelinePath,
			"--run-dir", r.runDir, "--stage", "product", "--input-id", "feature_request",
			"--source", "tracker_title_and_body", "--content-file", temporary); err != nil {
			return err
		}
		if r.stageHasInput("product", "source_issue") {
			sourcePath := filepath.Join(r.config.Workspace, "source-issue.json")
			source := r.config.SourceIssue
			if len(source) == 0 || string(source) == "{}" {
				source = mustJSON(map[string]any{"provider": "linear", "id": r.config.IssueID, "key": r.config.IssueKey,
					"url": r.config.IssueURL, "title": r.config.IssueTitle, "title_and_body": r.config.FeatureRequest})
			}
			if err := os.WriteFile(sourcePath, append(source, '\n'), 0o600); err != nil {
				return err
			}
			if _, err := r.harness(ctx, r.repo, "materialize-source", "--pipeline", pipelinePath,
				"--run-dir", r.runDir, "--stage", "product", "--input-id", "source_issue",
				"--source", "tracker_metadata", "--content-file", sourcePath); err != nil {
				return err
			}
		}
	}
	if len(r.config.HumanInput) > 0 && string(r.config.HumanInput) != "[]" {
		for _, stageID := range []string{"product", "arch"} {
			if !r.stageHasInput(stageID, "human_input") {
				continue
			}
			inputPath := filepath.Join(r.config.Workspace, "human-input-"+stageID+".json")
			if err := os.WriteFile(inputPath, r.config.HumanInput, 0o600); err != nil {
				return err
			}
			if _, err := r.harness(ctx, r.repo, "materialize-source", "--pipeline", pipelinePath,
				"--run-dir", r.runDir, "--stage", stageID, "--input-id", "human_input",
				"--source", "human_response", "--content-file", inputPath); err != nil {
				return err
			}
		}
	}
	_, err = r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json", string(mustJSON(map[string]any{
		"issue": map[string]any{"provider": "linear", "id": r.config.IssueID, "key": r.config.IssueKey,
			"url": r.config.IssueURL, "title": r.config.IssueTitle},
	})), "--event", "source.issue-materialized")
	if err != nil {
		return err
	}
	return nil
}

func (r *Runner) stageHasInput(stageID, inputID string) bool {
	for _, stage := range r.pipeline.Stages {
		if stage.ID != stageID {
			continue
		}
		for _, input := range stage.Inputs {
			if input.ID == inputID {
				return true
			}
		}
	}
	return false
}

func (r *Runner) executeStage(ctx context.Context, stage Stage) error {
	if _, err := r.harness(ctx, r.repo, "set-stage", "--run-dir", r.runDir, "--stage", stage.ID,
		"--status", "running", "--details-json", `{"summary":"cloud worker executing"}`); err != nil {
		return err
	}
	if err := r.event(ctx, "stage.started", "info", fmt.Sprintf("Stage %s started", stage.ID), stage.ID, nil); err != nil {
		return err
	}
	if err := r.checkpoint(ctx); err != nil {
		return err
	}
	if err := r.runHooks(ctx, stage, stage.Hooks.Before); err != nil {
		return err
	}
	var err error
	if stage.Mode == "ticket_parallel" {
		err = r.runTicketStage(ctx, stage)
	} else {
		err = r.runSingleStage(ctx, stage)
	}
	if err != nil {
		var repair *repairRequest
		var input *inputRequestSignal
		var policy *inputPolicyError
		if errors.As(err, &repair) || errors.As(err, &input) || errors.As(err, &policy) {
			return err
		}
		_ = r.runHooks(context.WithoutCancel(ctx), stage, stage.Hooks.OnFailure)
		return err
	}
	if err := r.runHooks(ctx, stage, stage.Hooks.After); err != nil {
		return err
	}
	if stage.ID != "pr" {
		if err := r.pushBranch(ctx); err != nil {
			return err
		}
	}
	if _, err := r.harness(ctx, r.repo, "set-stage", "--run-dir", r.runDir, "--stage", stage.ID,
		"--status", "completed", "--details-json", `{"summary":"validated and checkpointed"}`); err != nil {
		return err
	}
	if err := r.syncStage(ctx, stage); err != nil {
		return fmt.Errorf("required external synchronization: %w", err)
	}
	if err := r.checkpoint(ctx); err != nil {
		return err
	}
	return r.event(ctx, "stage.completed", "info", fmt.Sprintf("Stage %s completed", stage.ID), stage.ID, nil)
}

func (r *Runner) runSingleStage(ctx context.Context, stage Stage) error {
	if err := r.requireInputs(stage, r.runDir, ""); err != nil {
		return err
	}
	resultPath := filepath.Join(r.runDir, filepath.FromSlash(stage.Result.File))
	extra := ""
	if stage.ID == "arch" {
		extra = "The orchestrator will merge every required_owned_paths and additional_dependencies entry from a ready result into the product ticket plan and validate the revised DAG. Treat those declared additions as applied. The downstream docs stage owns documentation artifacts and the downstream QA stage owns Playwright acceptance evidence, so do not block solely because coder tickets omit those paths."
	}
	if stage.ID == "pr" {
		_, fetchErr := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), orchestratorGit, "fetch", "origin", r.config.BaseBranch)
		if fetchErr != nil {
			return fetchErr
		}
		_, rebaseErr := runCommand(ctx, r.repo, sanitizedEnvironment(""), orchestratorGit, "rebase", "origin/"+r.config.BaseBranch)
		if rebaseErr != nil {
			extra = "A rebase is in progress. Resolve it safely and verify the result. Do not push or call GitHub; the orchestrator owns those credentials. Return status blocked with the proposed PR title and complete body so the orchestrator can finish delivery."
		} else {
			extra = "Do not push or call GitHub; the orchestrator owns those credentials. Return status blocked with the proposed PR title and complete body so the orchestrator can finish delivery."
		}
	}
	if err := r.runCodex(ctx, r.repo, stage, "", resultPath, extra); err != nil {
		return err
	}
	if err := r.detectInputRequest(stage, resultPath); err != nil {
		return err
	}
	if stage.ID == "arch" {
		if err := r.reconcileArchitecture(ctx, resultPath); err != nil {
			return err
		}
	}
	if stage.ID == "pr" {
		if err := r.finalizePullRequest(ctx, resultPath); err != nil {
			return err
		}
	}
	if _, err := r.harness(ctx, r.repo, "materialize-result", "--pipeline", filepath.Join(r.repo, ".harness", "pipeline.yaml"),
		"--run-dir", r.runDir, "--stage", stage.ID, "--input", resultPath); err != nil {
		return err
	}
	if stage.ID == "product" {
		if err := r.recordTicketPlan(ctx); err != nil {
			return err
		}
	}
	if stage.ID == "qa" {
		var qa struct {
			Status string `json:"status"`
		}
		body, _ := os.ReadFile(resultPath)
		_ = json.Unmarshal(body, &qa)
		if qa.Status == "requeue" {
			return &repairRequest{resultPath: resultPath}
		}
	}
	return ensureSuccessfulResult(resultPath)
}

func (r *Runner) runTicketStage(ctx context.Context, stage Stage) error {
	pipelinePath := filepath.Join(r.repo, ".harness", "pipeline.yaml")
	if _, err := r.harness(ctx, r.repo, "materialize-generated-inputs", "--pipeline", pipelinePath,
		"--run-dir", r.runDir, "--stage", stage.ID); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(r.runDir, "artifacts", "ticket-plan.json"))
	if err != nil {
		return err
	}
	var tickets []ticket
	if err := json.Unmarshal(body, &tickets); err != nil {
		return err
	}
	done := map[string]bool{}
	for _, item := range tickets {
		resultPath := filepath.Join(r.runDir, "agent-output", "coder", item.Key+".json")
		if ensureSuccessfulResult(resultPath) == nil {
			done[item.Key] = true
		}
	}
	waves, err := ticketWavesWithDone(tickets, done)
	if err != nil {
		return err
	}
	for waveIndex, wave := range waves {
		runs := make([]*ticketRun, 0, len(wave))
		for _, item := range wave {
			worktree := filepath.Join(r.config.Workspace, "worktrees", safeName(item.Key))
			_ = os.RemoveAll(worktree)
			if _, err := runCommand(ctx, r.repo, sanitizedEnvironment(""), orchestratorGit, "worktree", "add", "--detach", worktree, "HEAD"); err != nil {
				return err
			}
			worktreeRun := runDirectory(worktree, r.pipeline, r.config.RunID)
			if err := copyDirectory(r.runDir, worktreeRun); err != nil {
				return err
			}
			runs = append(runs, &ticketRun{ticket: item, worktree: worktree})
		}
		if err := r.recordTicketWaveStarted(ctx, wave); err != nil {
			r.cleanupWorktrees(ctx, runs)
			return fmt.Errorf("claim ticket wave: %w", err)
		}
		if err := r.syncTicketProgress(ctx, stage.ID); err != nil {
			r.cleanupWorktrees(ctx, runs)
			return err
		}
		for _, current := range runs {
			if err := r.event(ctx, "ticket.started", "info", "Coder subagent claimed ticket", stage.ID,
				map[string]any{"ticket_key": current.ticket.Key, "depends_on": current.ticket.DependsOn, "owner": r.config.LeaseOwner}); err != nil {
				current.err = err
			}
			if current.err == nil {
				worktreeRun := runDirectory(current.worktree, r.pipeline, r.config.RunID)
				if err := r.requireInputs(stage, worktreeRun, current.ticket.Key); err != nil {
					current.err = err
				}
			}
		}
		var coordinatorErr error
		if firstTicketRunError(runs) == nil {
			coordinatorErr = r.runCodexTicketWave(ctx, stage, waveIndex+1, runs)
		}
		for _, current := range runs {
			if current.err == nil {
				worktreeRun := runDirectory(current.worktree, r.pipeline, r.config.RunID)
				resultPath := filepath.Join(worktreeRun, filepath.FromSlash(replaceTicket(stage.Result.File, current.ticket.Key)))
				if err := r.detectInputRequest(stage, resultPath); err != nil {
					current.err = err
					if coordinatorErr != nil {
						current.err = fmt.Errorf("coordinator failed: %v; ticket %s result: %w", coordinatorErr, current.ticket.Key, err)
					}
				}
				if current.err != nil {
					continue
				}
				current.result, current.err = os.ReadFile(resultPath)
				if current.err != nil {
					continue
				}
				if _, err := r.harness(ctx, current.worktree, "materialize-result", "--pipeline", filepath.Join(current.worktree, ".harness", "pipeline.yaml"),
					"--run-dir", worktreeRun, "--stage", stage.ID, "--input", resultPath, "--ticket-key", current.ticket.Key); err != nil {
					current.err = err
					continue
				}
				var output struct {
					Status  string `json:"status"`
					Commit  string `json:"commit"`
					Blocker any    `json:"blocker"`
				}
				if err := json.Unmarshal(current.result, &output); err != nil {
					current.err = err
					continue
				}
				if output.Status != "completed" || output.Commit == "" {
					current.blocker = ticketBlockerText(output.Blocker)
					if current.blocker != "" {
						current.err = fmt.Errorf("ticket %s blocked: %s", current.ticket.Key, current.blocker)
					} else {
						current.err = fmt.Errorf("ticket %s did not produce a completed commit", current.ticket.Key)
					}
					continue
				}
				current.commit = output.Commit
			}
			if current.err != nil {
				if current.blocker == "" {
					current.blocker = current.err.Error()
				}
				if len(current.result) > 0 {
					if _, err := preserveTicketResult(r.runDir, stage, current.ticket.Key, current.result); err != nil {
						current.err = errors.Join(current.err, fmt.Errorf("preserve ticket result: %w", err))
					}
				}
				if err := r.recordTicketFailure(context.WithoutCancel(ctx), current.ticket, current.blocker); err != nil {
					current.err = errors.Join(current.err, fmt.Errorf("checkpoint ticket failure: %w", err))
				}
				_ = r.event(context.WithoutCancel(ctx), "ticket.failed", "error", current.err.Error(), stage.ID,
					map[string]any{"ticket_key": current.ticket.Key, "depends_on": current.ticket.DependsOn,
						"owner": r.config.LeaseOwner, "blocker": current.blocker})
			}
		}
		if firstTicketRunError(runs) != nil {
			if err := r.syncTicketProgress(context.WithoutCancel(ctx), stage.ID); err != nil {
				for _, current := range runs {
					if current.err != nil {
						current.err = errors.Join(current.err, fmt.Errorf("sync ticket failure: %w", err))
						break
					}
				}
			}
		}
		sort.Slice(runs, func(i, j int) bool { return runs[i].ticket.Key < runs[j].ticket.Key })
		var firstTicketError error
		integrated := false
		for _, current := range runs {
			if current.err != nil {
				if firstTicketError == nil {
					firstTicketError = current.err
				}
				continue
			}
			alreadyIntegrated, err := gitCommitAlreadyIntegrated(ctx, r.repo, current.commit)
			if err != nil {
				r.cleanupWorktrees(ctx, runs)
				return fmt.Errorf("inspect ticket %s commit: %w", current.ticket.Key, err)
			}
			if !alreadyIntegrated {
				_, err = runCommand(ctx, r.repo, sanitizedEnvironment(""), orchestratorGit, "cherry-pick", current.commit)
			}
			if err != nil {
				_, _ = runCommand(context.WithoutCancel(ctx), r.repo, sanitizedEnvironment(""), orchestratorGit, "cherry-pick", "--abort")
				r.cleanupWorktrees(ctx, runs)
				return fmt.Errorf("integrate ticket %s: %w", current.ticket.Key, err)
			}
			temporary := filepath.Join(r.config.Workspace, safeName(current.ticket.Key)+"-result.json")
			if err := os.WriteFile(temporary, current.result, 0o600); err != nil {
				return err
			}
			if _, err := r.harness(ctx, r.repo, "materialize-result", "--pipeline", pipelinePath,
				"--run-dir", r.runDir, "--stage", stage.ID, "--input", temporary, "--ticket-key", current.ticket.Key); err != nil {
				return err
			}
			if err := r.recordTicketCompletion(ctx, current.ticket, current.commit); err != nil {
				return err
			}
			if err := r.syncTicketProgress(ctx, stage.ID); err != nil {
				return fmt.Errorf("sync completed ticket %s: %w", current.ticket.Key, err)
			}
			message := "Ticket commit integrated"
			if alreadyIntegrated {
				message = "Existing ticket repair adopted"
			}
			_ = r.event(ctx, "ticket.completed", "info", message, stage.ID,
				map[string]any{"ticket_key": current.ticket.Key, "depends_on": current.ticket.DependsOn,
					"owner": r.config.LeaseOwner, "commit": current.commit})
			integrated = true
		}
		if firstTicketError == nil && coordinatorErr != nil {
			firstTicketError = coordinatorErr
		}
		r.cleanupWorktrees(ctx, runs)
		if integrated {
			if err := r.pushBranch(ctx); err != nil {
				return err
			}
			if err := r.checkpoint(ctx); err != nil {
				return err
			}
		}
		if firstTicketError != nil {
			return firstTicketError
		}
	}
	return nil
}

func ticketBlockerText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	body, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return string(body)
}

func preserveTicketResult(runDir string, stage Stage, ticketKey string, result []byte) (string, error) {
	resultPath := filepath.Join(runDir, filepath.FromSlash(replaceTicket(stage.Result.File, ticketKey)))
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o700); err != nil {
		return resultPath, err
	}
	return resultPath, os.WriteFile(resultPath, result, 0o600)
}

func firstTicketRunError(runs []*ticketRun) error {
	for _, current := range runs {
		if current.err != nil {
			return current.err
		}
	}
	return nil
}

func (r *Runner) cleanupWorktrees(ctx context.Context, runs []*ticketRun) {
	for _, current := range runs {
		_, _ = runCommand(context.WithoutCancel(ctx), r.repo, sanitizedEnvironment(""), orchestratorGit, "worktree", "remove", "--force", current.worktree)
	}
}

func (r *Runner) finalizePullRequest(ctx context.Context, resultPath string) error {
	status, err := runCommand(ctx, r.repo, sanitizedEnvironment(""), orchestratorGit, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(status)) != "" {
		return errors.New("PR worktree is not clean after verification")
	}
	wasPublished := r.branchPublished
	if wasPublished {
		head, err := runCommand(ctx, r.repo, sanitizedEnvironment(""), orchestratorGit, "rev-parse", "--short=12", "HEAD")
		if err != nil {
			return err
		}
		r.deliveryBranch = r.baseRunBranchName() + "-pr-" + strings.TrimSpace(string(head))
		if _, err := runCommand(ctx, r.repo, sanitizedEnvironment(""), orchestratorGit, "checkout", "-B", r.deliveryBranch); err != nil {
			return err
		}
		r.branchPublished = false
	}
	if err := r.pushBranch(ctx); err != nil {
		return err
	}
	body, err := os.ReadFile(resultPath)
	if err != nil {
		return err
	}
	var output map[string]any
	if err := json.Unmarshal(body, &output); err != nil {
		return err
	}
	pullRequest, _ := output["pull_request"].(map[string]any)
	title := fmt.Sprint(pullRequest["title"])
	prBody := fmt.Sprint(pullRequest["body"])
	if title == "" || title == "<nil>" || prBody == "" || prBody == "<nil>" {
		return errors.New("PR agent did not provide a title and body")
	}
	env := append(sanitizedEnvironment(""), "GH_TOKEN="+r.githubToken)
	repository := r.config.GitHubOwner + "/" + r.config.GitHubRepo
	view, viewErr := runCommand(ctx, r.repo, env, "gh", "pr", "view", r.branchName(), "--repo", repository, "--json", "number,url,title,body")
	if viewErr != nil {
		if _, err := runCommand(ctx, r.repo, env, "gh", "pr", "create", "--draft", "--repo", repository,
			"--base", r.config.BaseBranch, "--head", r.branchName(), "--title", title, "--body", prBody); err != nil {
			return err
		}
		view, err = runCommand(ctx, r.repo, env, "gh", "pr", "view", r.branchName(), "--repo", repository, "--json", "number,url,title,body")
		if err != nil {
			return err
		}
	}
	var canonical map[string]any
	if err := json.Unmarshal(view, &canonical); err != nil {
		return err
	}
	output["status"], output["blocker"] = "created", nil
	mode := "normal"
	if wasPublished {
		mode = "delivery-branch"
	}
	output["push"] = map[string]any{"status": "PASS", "mode": mode}
	output["pull_request"] = canonical
	url := fmt.Sprint(canonical["url"])
	if _, err := r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json",
		string(mustJSON(map[string]any{"git": map[string]any{"branch": r.branchName(), "base": r.config.BaseBranch, "pr_url": url}})), "--event", "pr.created"); err != nil {
		return err
	}
	if err := r.event(ctx, "pr.created", "info", "Draft pull request created", "pr", map[string]any{"branch": r.branchName(), "url": url}); err != nil {
		return err
	}
	updated, _ := json.MarshalIndent(output, "", "  ")
	return os.WriteFile(resultPath, append(updated, '\n'), 0o600)
}

func (r *Runner) pushBranch(ctx context.Context) error {
	args := []string{"push", "--set-upstream", "origin", r.branchName()}
	_, err := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), orchestratorGit, args...)
	if err == nil {
		r.branchPublished = true
	}
	return err
}

func (r *Runner) runHooks(ctx context.Context, stage Stage, hooks []Hook) error {
	for _, hook := range hooks {
		if hook.Cwd == "" {
			hook.Cwd = "."
		}
		if hook.TimeoutSeconds == 0 {
			hook.TimeoutSeconds = 300
		}
		spec, _ := json.Marshal(hook)
		env, _ := json.Marshal(map[string]string{
			"HARNESS_RUN_ID": r.config.RunID, "HARNESS_ISSUE_KEY": r.config.IssueKey,
			"HARNESS_STAGE": stage.ID, "HARNESS_ARTIFACT_DIR": filepath.Join(r.runDir, "artifacts"),
			"HARNESS_WORKTREE": r.repo,
		})
		if _, err := r.harness(ctx, r.repo, "run-hook", "--repo", r.repo, "--spec-json", string(spec), "--env-json", string(env)); err != nil {
			return fmt.Errorf("hook %s: %w", hook.ID, err)
		}
	}
	return nil
}

func (r *Runner) requireInputs(stage Stage, runDir, ticketKey string) error {
	for _, input := range stage.Inputs {
		if !input.Required {
			continue
		}
		value := filepath.Join(runDir, filepath.FromSlash(replaceTicket(input.File, ticketKey)))
		if strings.ContainsAny(value, "*?[") {
			matches, _ := filepath.Glob(value)
			if len(matches) == 0 {
				return fmt.Errorf("stage %s is missing input %s", stage.ID, input.ID)
			}
		} else if _, err := os.Stat(value); err != nil {
			return fmt.Errorf("stage %s is missing input %s: %w", stage.ID, input.ID, err)
		}
	}
	return nil
}

func (r *Runner) checkpoint(ctx context.Context) error {
	archive := filepath.Join(r.config.Workspace, "journal-upload.tar.gz")
	if err := archiveDirectory(r.runDir, archive); err != nil {
		return err
	}
	return r.client.uploadJournal(ctx, archive)
}

func (r *Runner) pause(ctx context.Context, stage Stage, cause error) error {
	_, _ = r.harness(context.WithoutCancel(ctx), r.repo, "set-stage", "--run-dir", r.runDir, "--stage", stage.ID,
		"--status", "blocked", "--details-json", string(mustJSON(map[string]string{"error": cause.Error()})))
	checkpointCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	_ = r.checkpoint(checkpointCtx)
	_ = r.event(checkpointCtx, "run.paused", "error", cause.Error(), stage.ID, nil)
	return cause
}

func (r *Runner) detectInputRequest(stage Stage, resultPath string) error {
	body, err := os.ReadFile(resultPath)
	if err != nil {
		return err
	}
	var output struct {
		Status       string `json:"status"`
		InputRequest struct {
			Summary   string                `json:"summary"`
			Questions []model.InputQuestion `json:"questions"`
		} `json:"input_request"`
	}
	if err := json.Unmarshal(body, &output); err != nil {
		return err
	}
	if output.Status != "needs_input" {
		return nil
	}
	if stage.ID != "product" && stage.ID != "arch" {
		return &inputPolicyError{message: fmt.Sprintf("stage %s is not permitted to request human input", stage.ID)}
	}
	if r.hasHumanInputForStage(stage.ID) {
		return &inputPolicyError{message: fmt.Sprintf("stage %s exhausted its single permitted human-input round", stage.ID)}
	}
	if err := validateWorkerInputRequest(output.InputRequest.Summary, output.InputRequest.Questions); err != nil {
		return &inputPolicyError{message: err.Error()}
	}
	return &inputRequestSignal{request: model.InputRequest{RunID: r.config.RunID, Stage: stage.ID, Round: 1,
		Summary: output.InputRequest.Summary, Questions: output.InputRequest.Questions}}
}

func validateWorkerInputRequest(summary string, questions []model.InputQuestion) error {
	if strings.TrimSpace(summary) == "" || len(questions) == 0 || len(questions) > 3 {
		return errors.New("needs_input result requires a summary and one to three structured questions")
	}
	questionIDs := map[string]bool{}
	for _, question := range questions {
		if strings.TrimSpace(question.ID) == "" || strings.TrimSpace(question.Prompt) == "" || questionIDs[question.ID] {
			return errors.New("needs_input questions require unique ids and prompts")
		}
		questionIDs[question.ID] = true
		if len(question.Options) < 2 || len(question.Options) > 3 || !question.AllowFreeText {
			return fmt.Errorf("needs_input question %s requires two or three options and a free-text alternative", question.ID)
		}
		optionIDs, recommended := map[string]bool{}, 0
		for _, option := range question.Options {
			if strings.TrimSpace(option.ID) == "" || strings.TrimSpace(option.Label) == "" || optionIDs[option.ID] {
				return fmt.Errorf("needs_input question %s has an invalid option", question.ID)
			}
			optionIDs[option.ID] = true
			if option.Recommended {
				recommended++
			}
		}
		if recommended != 1 {
			return fmt.Errorf("needs_input question %s requires exactly one recommended option", question.ID)
		}
	}
	return nil
}

func (r *Runner) hasHumanInputForStage(stage string) bool {
	var values []struct {
		Request struct {
			Stage string `json:"stage"`
		} `json:"request"`
	}
	if json.Unmarshal(r.config.HumanInput, &values) != nil {
		return false
	}
	for _, value := range values {
		if value.Request.Stage == stage {
			return true
		}
	}
	return false
}

func (r *Runner) awaitInput(ctx context.Context, stage Stage, request model.InputRequest) error {
	details := map[string]any{"summary": request.Summary, "questions": request.Questions, "round": 1}
	if _, err := r.harness(context.WithoutCancel(ctx), r.repo, "set-stage", "--run-dir", r.runDir, "--stage", stage.ID,
		"--status", "waiting_for_input", "--details-json", string(mustJSON(details))); err != nil {
		return err
	}
	checkpointCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := r.checkpoint(checkpointCtx); err != nil {
		return err
	}
	return r.event(checkpointCtx, "human_input.requested", "info", "Stage requested human input; sandbox may be stopped", stage.ID, details)
}

func (r *Runner) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.client.heartbeat(ctx, r.config.LeaseOwner); err != nil {
				r.logger.Warn("control-plane heartbeat failed", "error", err)
			}
		}
	}
}

func (r *Runner) event(ctx context.Context, eventType, level, message, stage string, payload any) error {
	var raw json.RawMessage
	if payload != nil {
		raw = mustJSON(payload)
	}
	return r.client.event(ctx, model.Event{RunID: r.config.RunID, SourceIssueID: r.config.IssueID,
		Stage: stage, Type: eventType, Level: level, Message: message, Payload: raw})
}

func (r *Runner) harness(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	command := append([]string{r.config.Harnessctl}, args...)
	return runCommand(ctx, cwd, sanitizedEnvironment(""), "python3", command...)
}

func (r *Runner) stageCompleted(stage string) (bool, error) {
	state, err := r.state()
	if err != nil {
		return false, err
	}
	details, _ := state.Stages[stage].(map[string]any)
	return details["status"] == "completed", nil
}

func (r *Runner) requireDependencies(stage Stage) error {
	state, err := r.state()
	if err != nil {
		return err
	}
	for _, dependency := range stage.Needs {
		details, _ := state.Stages[dependency].(map[string]any)
		if details["status"] != "completed" {
			return fmt.Errorf("stage %s requires completed stage %s", stage.ID, dependency)
		}
	}
	return nil
}

type journalState struct {
	Stages map[string]any `json:"stages"`
	Status string         `json:"status"`
}

func (r *Runner) state() (journalState, error) {
	var value journalState
	body, err := os.ReadFile(filepath.Join(r.runDir, "state.json"))
	if err == nil {
		err = json.Unmarshal(body, &value)
	}
	return value, err
}

func (r *Runner) branchName() string {
	if r.deliveryBranch != "" {
		return r.deliveryBranch
	}
	return r.baseRunBranchName()
}

func (r *Runner) baseRunBranchName() string {
	suffix := r.config.RunID
	if len(suffix) > 12 {
		suffix = suffix[len(suffix)-12:]
	}
	// Railway's sandbox Git policy permits pushes only beneath sandbox/*.
	// Keep the human-readable harness prefix after that provider-owned boundary.
	return "sandbox/agent-harness-" + strings.ToLower(safeName(r.config.IssueKey)) + "-" + suffix
}

func ensureSuccessfulResult(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var value struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	switch value.Status {
	case "ready", "completed", "passed", "created":
		return nil
	default:
		return fmt.Errorf("agent result is not successful: %s", value.Status)
	}
}

func mustJSON(value any) json.RawMessage {
	body, _ := json.Marshal(value)
	return body
}
