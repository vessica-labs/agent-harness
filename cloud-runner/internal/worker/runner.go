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
	"strings"
	"sync"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

type Runner struct {
	config          Config
	client          *controlClient
	logger          *slog.Logger
	repo            string
	runDir          string
	codexHome       string
	codexSessions   []runtimeCodexSession
	codexPool       chan runtimeCodexSession
	pipeline        Pipeline
	localLease      string
	githubToken     string
	branchPublished bool
}

type runtimeCodexSession struct {
	id   string
	home string
	auth []byte
}

func New(config Config, logger *slog.Logger) *Runner {
	return &Runner{config: config, client: newControlClient(config), logger: logger}
}

func (r *Runner) Run(ctx context.Context) (runErr error) {
	if err := r.prepareFilesystem(); err != nil {
		return err
	}
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

	var err error
	r.githubToken, err = r.client.githubToken(ctx)
	if err != nil {
		return fmt.Errorf("obtain repository credential: %w", err)
	}
	if err := r.checkout(ctx); err != nil {
		return err
	}
	pipelinePath := filepath.Join(r.repo, ".harness", "pipeline.yaml")
	r.pipeline, err = loadPipeline(pipelinePath)
	if err != nil {
		return fmt.Errorf("load pipeline: %w", err)
	}
	r.runDir = runDirectory(r.repo, r.pipeline, r.config.RunID)
	if err := r.restoreOrInitialize(ctx, pipelinePath); err != nil {
		return err
	}
	if _, err := r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json",
		string(mustJSON(map[string]any{"git": map[string]any{"branch": r.branchName(), "base": r.config.BaseBranch}})), "--event", "git.branch-prepared"); err != nil {
		return err
	}

	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go r.heartbeat(heartbeatCtx)
	for _, stage := range r.pipeline.Stages {
		if err := r.event(ctx, "pipeline.stage", "info", "Pipeline stage registered", stage.ID,
			map[string]any{"needs": stage.Needs, "mode": stage.Mode, "parallelism": stage.Parallelism, "agent": stage.Agent}); err != nil {
			return err
		}
	}
	if err := r.event(ctx, "run.started", "info", "Sandbox worker began pipeline execution", "", nil); err != nil {
		return err
	}
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
	return r.event(ctx, "run.completed", "info", "Pipeline completed and produced a draft pull request", "", nil)
}

func (r *Runner) executeStageWithRetries(ctx context.Context, stage Stage) error {
	const maxAttempts = 3
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		last = r.executeStage(ctx, stage)
		if last == nil {
			return nil
		}
		var repair *repairRequest
		if errors.As(last, &repair) || ctx.Err() != nil {
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
	r.codexPool = make(chan runtimeCodexSession, len(r.config.CodexSessions))
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
		r.codexPool <- session
	}
	r.codexHome = r.codexSessions[0].home
	return nil
}

func (r *Runner) checkout(ctx context.Context) error {
	branch := r.branchName()
	if _, err := os.Stat(filepath.Join(r.repo, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.RemoveAll(r.repo); err != nil {
			return err
		}
		url := fmt.Sprintf("https://github.com/%s/%s.git", r.config.GitHubOwner, r.config.GitHubRepo)
		if _, err := runCommand(ctx, r.config.Workspace, gitEnvironment(r.githubToken), "git", "clone", "--origin", "origin", url, r.repo); err != nil {
			return fmt.Errorf("clone repository: %w", err)
		}
	}
	if _, err := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), "git", "fetch", "origin", "--prune"); err != nil {
		return err
	}
	_, remoteErr := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), "git", "ls-remote", "--exit-code", "--heads", "origin", branch)
	if remoteErr == nil {
		r.branchPublished = true
		_, err := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), "git", "checkout", "-B", branch, "origin/"+branch)
		return err
	}
	_, err := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), "git", "checkout", "-B", branch, "origin/"+r.config.BaseBranch)
	return err
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
	}
	return nil
}

func (r *Runner) executeStage(ctx context.Context, stage Stage) error {
	if _, err := r.harness(ctx, r.repo, "set-stage", "--run-dir", r.runDir, "--stage", stage.ID,
		"--status", "running", "--details-json", `{"summary":"cloud worker executing"}`); err != nil {
		return err
	}
	if err := r.event(ctx, "stage.started", "info", "Stage started", stage.ID, nil); err != nil {
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
		if errors.As(err, &repair) {
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
	return r.event(ctx, "stage.completed", "info", "Stage completed", stage.ID, nil)
}

func (r *Runner) runSingleStage(ctx context.Context, stage Stage) error {
	if err := r.requireInputs(stage, r.runDir, ""); err != nil {
		return err
	}
	resultPath := filepath.Join(r.runDir, filepath.FromSlash(stage.Result.File))
	extra := ""
	if stage.ID == "pr" {
		_, fetchErr := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), "git", "fetch", "origin", r.config.BaseBranch)
		if fetchErr != nil {
			return fetchErr
		}
		_, rebaseErr := runCommand(ctx, r.repo, sanitizedEnvironment(""), "git", "rebase", "origin/"+r.config.BaseBranch)
		if rebaseErr != nil {
			extra = "A rebase is in progress. Resolve it safely and verify the result. Do not push or call GitHub; the orchestrator owns those credentials. Return status blocked with the proposed PR title and complete body so the orchestrator can finish delivery."
		} else {
			extra = "Do not push or call GitHub; the orchestrator owns those credentials. Return status blocked with the proposed PR title and complete body so the orchestrator can finish delivery."
		}
	}
	if err := r.runCodex(ctx, r.repo, stage, "", resultPath, extra); err != nil {
		return err
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
	for _, wave := range waves {
		runs := make([]*ticketRun, 0, len(wave))
		for _, item := range wave {
			worktree := filepath.Join(r.config.Workspace, "worktrees", safeName(item.Key))
			_ = os.RemoveAll(worktree)
			if _, err := runCommand(ctx, r.repo, sanitizedEnvironment(""), "git", "worktree", "add", "--detach", worktree, "HEAD"); err != nil {
				return err
			}
			worktreeRun := runDirectory(worktree, r.pipeline, r.config.RunID)
			if err := copyDirectory(r.runDir, worktreeRun); err != nil {
				return err
			}
			runs = append(runs, &ticketRun{ticket: item, worktree: worktree})
		}
		parallelism := stage.Parallelism
		if !r.config.CodexParallelSafe && parallelism > len(r.codexSessions) {
			parallelism = len(r.codexSessions)
		}
		semaphore := make(chan struct{}, parallelism)
		var wait sync.WaitGroup
		for _, current := range runs {
			wait.Add(1)
			go func(current *ticketRun) {
				defer wait.Done()
				defer func() {
					if current.err != nil {
						_ = r.event(context.WithoutCancel(ctx), "ticket.failed", "error", current.err.Error(), stage.ID,
							map[string]any{"ticket_key": current.ticket.Key, "depends_on": current.ticket.DependsOn, "owner": r.config.LeaseOwner})
					}
				}()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				if err := r.event(ctx, "ticket.started", "info", "Coder agent claimed ticket", stage.ID,
					map[string]any{"ticket_key": current.ticket.Key, "depends_on": current.ticket.DependsOn, "owner": r.config.LeaseOwner}); err != nil {
					current.err = err
					return
				}
				worktreeRun := runDirectory(current.worktree, r.pipeline, r.config.RunID)
				if err := r.requireInputs(stage, worktreeRun, current.ticket.Key); err != nil {
					current.err = err
					return
				}
				resultPath := filepath.Join(worktreeRun, filepath.FromSlash(replaceTicket(stage.Result.File, current.ticket.Key)))
				if err := r.runCodex(ctx, current.worktree, stage, current.ticket.Key, resultPath, "Implement only this claimed ticket and do not push."); err != nil {
					current.err = err
					return
				}
				if _, err := r.harness(ctx, current.worktree, "materialize-result", "--pipeline", filepath.Join(current.worktree, ".harness", "pipeline.yaml"),
					"--run-dir", worktreeRun, "--stage", stage.ID, "--input", resultPath, "--ticket-key", current.ticket.Key); err != nil {
					current.err = err
					return
				}
				current.result, current.err = os.ReadFile(resultPath)
				if current.err != nil {
					return
				}
				var output struct {
					Status string `json:"status"`
					Commit string `json:"commit"`
				}
				if err := json.Unmarshal(current.result, &output); err != nil {
					current.err = err
					return
				}
				if output.Status != "completed" || output.Commit == "" {
					current.err = fmt.Errorf("ticket %s did not produce a completed commit", current.ticket.Key)
					return
				}
				current.commit = output.Commit
			}(current)
		}
		wait.Wait()
		sort.Slice(runs, func(i, j int) bool { return runs[i].ticket.Key < runs[j].ticket.Key })
		for _, current := range runs {
			if current.err != nil {
				r.cleanupWorktrees(ctx, runs)
				return current.err
			}
			if _, err := runCommand(ctx, r.repo, sanitizedEnvironment(""), "git", "cherry-pick", current.commit); err != nil {
				_, _ = runCommand(context.WithoutCancel(ctx), r.repo, sanitizedEnvironment(""), "git", "cherry-pick", "--abort")
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
			_ = r.event(ctx, "ticket.completed", "info", "Ticket commit integrated", stage.ID,
				map[string]any{"ticket_key": current.ticket.Key, "depends_on": current.ticket.DependsOn,
					"owner": r.config.LeaseOwner, "commit": current.commit})
		}
		r.cleanupWorktrees(ctx, runs)
		if err := r.pushBranch(ctx); err != nil {
			return err
		}
		if err := r.checkpoint(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) cleanupWorktrees(ctx context.Context, runs []*ticketRun) {
	for _, current := range runs {
		_, _ = runCommand(context.WithoutCancel(ctx), r.repo, sanitizedEnvironment(""), "git", "worktree", "remove", "--force", current.worktree)
	}
}

func (r *Runner) finalizePullRequest(ctx context.Context, resultPath string) error {
	status, err := runCommand(ctx, r.repo, sanitizedEnvironment(""), "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(status)) != "" {
		return errors.New("PR worktree is not clean after verification")
	}
	wasPublished := r.branchPublished
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
		mode = "force-with-lease"
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
	if r.branchPublished {
		args = []string{"push", "--force-with-lease", "--set-upstream", "origin", r.branchName()}
	}
	_, err := runCommand(ctx, r.repo, gitEnvironment(r.githubToken), "git", args...)
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
	suffix := r.config.RunID
	if len(suffix) > 12 {
		suffix = suffix[len(suffix)-12:]
	}
	return "agent-harness/" + strings.ToLower(safeName(r.config.IssueKey)) + "-" + suffix
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
