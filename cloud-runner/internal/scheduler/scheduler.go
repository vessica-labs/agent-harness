package scheduler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/preview"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/sandbox"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

type Config struct {
	Owner                 string
	ControlPlaneURL       string
	Checkpoint            string
	RepositoryCheckpoints map[string]string
	MaxActiveRuns         int
	LeaseDuration         time.Duration
	AuthLeaseDuration     time.Duration
	PollInterval          time.Duration
	HeartbeatInterval     time.Duration
	StartupTimeout        time.Duration
	IdleTimeout           int
	CodexModel            string
	PlaywrightWorkers     int
}

type Scheduler struct {
	store       store.Store
	sandbox     sandbox.Provider
	box         *secure.Box
	broker      *events.Broker
	config      Config
	logger      *slog.Logger
	authRetryAt atomic.Int64
	preview     *preview.Manager
}

// SetPreviewManager lets the cleanup loop retain completed-run sandboxes
// while their previews are alive and tear them down at expiry.
func (s *Scheduler) SetPreviewManager(manager *preview.Manager) {
	s.preview = manager
}

func New(values store.Store, provider sandbox.Provider, box *secure.Box, broker *events.Broker, config Config, logger *slog.Logger) *Scheduler {
	if config.MaxActiveRuns <= 0 {
		config.MaxActiveRuns = 3
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = 15 * time.Minute
	}
	if config.AuthLeaseDuration <= 0 {
		config.AuthLeaseDuration = 24 * time.Hour
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 2 * time.Second
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 5 * time.Minute
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = 90 * time.Second
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = 120
	}
	if config.Owner == "" {
		config.Owner = "control-plane"
	}
	if config.CodexModel == "" {
		config.CodexModel = "gpt-5.6-sol"
	}
	if config.PlaywrightWorkers <= 0 {
		config.PlaywrightWorkers = 2
	}
	return &Scheduler{store: values, sandbox: provider, box: box, broker: broker, config: config, logger: logger}
}

func (s *Scheduler) Run(ctx context.Context) {
	claimTicker := time.NewTicker(s.config.PollInterval)
	heartbeatTicker := time.NewTicker(s.config.HeartbeatInterval)
	defer claimTicker.Stop()
	defer heartbeatTicker.Stop()
	s.claim(ctx)
	s.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-claimTicker.C:
			s.claim(ctx)
		case <-heartbeatTicker.C:
			s.reconcile(ctx)
		}
	}
}

func (s *Scheduler) claim(ctx context.Context) {
	if time.Now().UnixNano() < s.authRetryAt.Load() {
		return
	}
	for {
		run, err := s.store.ClaimNextRun(ctx, s.config.Owner, s.config.MaxActiveRuns, s.config.LeaseDuration)
		if errors.Is(err, store.ErrNoRunnableRun) {
			return
		}
		if err != nil {
			s.logger.Error("claim run", "error", err)
			return
		}
		go s.launch(ctx, run)
	}
}

func (s *Scheduler) launch(ctx context.Context, run model.Run) {
	requestedAt := time.Now()
	s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID,
		Type: "run.infrastructure.stage", Level: "info", Message: "Startup phase completed",
		Payload: mustJSON(map[string]any{"stage": "control_plane_queue", "duration_ms": max(time.Since(run.CreatedAt).Milliseconds(), 0), "status": "completed"})})
	authStarted := time.Now()
	// A source-issue run owns exactly one top-level Codex session. Independent
	// source issues scale through MaxActiveRuns and independent auth slots;
	// ticket-level fan-out happens inside that session through native subagents.
	slots, err := s.store.LeaseAuthSlots(ctx, run.ID, 1, s.config.AuthLeaseDuration)
	if errors.Is(err, store.ErrNoAuthSlot) {
		s.authRetryAt.Store(time.Now().Add(30 * time.Second).UnixNano())
		_ = s.store.RequeueRun(ctx, run.ID, "auth_slot_unavailable")
		s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID,
			Type: "run.queued", Level: "warning", Message: "Waiting for an independent Codex authentication slot"})
		return
	}
	if err != nil {
		s.pause(ctx, run, "auth_slot_error", err.Error())
		return
	}
	s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID,
		Type: "run.infrastructure.stage", Level: "info", Message: "Startup phase completed",
		Payload: mustJSON(map[string]any{"stage": "auth_slot_lease", "duration_ms": time.Since(authStarted).Milliseconds(), "status": "completed", "slots": len(slots)})})
	slotIDs := make([]string, 0, len(slots))
	for _, slot := range slots {
		slotIDs = append(slotIDs, slot.ID)
	}
	if err := s.store.SetAuthSlot(ctx, run.ID, strings.Join(slotIDs, ",")); err != nil {
		s.releaseSlots(ctx, run.ID, slots, err.Error())
		s.pause(ctx, run, "auth_slot_assignment_failed", err.Error())
		return
	}
	if s.terminal(ctx, run.ID) {
		s.releaseSlots(ctx, run.ID, slots, "")
		return
	}
	type authSession struct {
		ID   string `json:"id"`
		Auth []byte `json:"auth"`
	}
	sessions := make([]authSession, 0, len(slots))
	for _, slot := range slots {
		auth, openErr := s.box.Open(slot.Ciphertext, secure.Purpose("codex", slot.ID))
		if openErr != nil {
			s.releaseSlots(ctx, run.ID, slots, openErr.Error())
			s.pause(ctx, run, "auth_slot_decrypt_failed", openErr.Error())
			return
		}
		sessions = append(sessions, authSession{ID: slot.ID, Auth: auth})
	}
	sessionsJSON, _ := json.Marshal(sessions)
	repository, err := s.store.GetRepository(ctx, run.RepositoryID)
	if err != nil {
		s.releaseSlots(ctx, run.ID, slots, "")
		s.pause(ctx, run, "repository_missing", err.Error())
		return
	}
	inputContext := make([]map[string]any, 0)
	if requests, listErr := s.store.ListInputRequests(ctx, model.InputRequestFilter{RunID: run.ID, Limit: 100}); listErr == nil {
		for _, request := range requests {
			responses, _ := s.store.ListInputResponses(ctx, request.ID)
			if len(responses) > 0 {
				inputContext = append(inputContext, map[string]any{"request": request, "responses": responses})
			}
		}
	}
	inputJSON, _ := json.Marshal(inputContext)
	capability, err := s.box.MintCapability(run.ID, time.Now().Add(7*24*time.Hour))
	if err != nil {
		s.releaseSlots(ctx, run.ID, slots, "")
		s.pause(ctx, run, "capability_failed", err.Error())
		return
	}
	variables := map[string]string{
		"HARNESS_RUN_ID": run.ID, "HARNESS_ISSUE_KEY": run.SourceIssueKey,
		"HARNESS_ISSUE_ID": run.SourceIssueID, "HARNESS_ISSUE_URL": run.SourceIssueURL,
		"HARNESS_ISSUE_TITLE": run.SourceIssueTitle, "HARNESS_CONTROL_URL": s.config.ControlPlaneURL,
		"HARNESS_RUN_CAPABILITY": capability, "HARNESS_LEASE_OWNER": s.config.Owner,
		"HARNESS_REPOSITORY_ID": repository.ID, "HARNESS_GITHUB_OWNER": repository.GitHubOwner,
		"HARNESS_GITHUB_REPO": repository.GitHubRepo, "HARNESS_BASE_BRANCH": repository.BaseBranch,
		"HARNESS_FEATURE_REQUEST_B64": base64.StdEncoding.EncodeToString([]byte(run.FeatureRequest)),
		"HARNESS_SOURCE_ISSUE_B64":    base64.StdEncoding.EncodeToString(run.Metadata),
		"HARNESS_HUMAN_INPUT_B64":     base64.StdEncoding.EncodeToString(inputJSON),
		"HARNESS_CODEX_SESSIONS_B64":  base64.StdEncoding.EncodeToString(sessionsJSON),
		"HARNESS_CODEX_AUTH_B64":      base64.StdEncoding.EncodeToString(sessions[0].Auth),
		"HARNESS_CODEX_AUTH_SLOT":     sessions[0].ID, "HARNESS_ATTEMPT": strconv.Itoa(run.Attempt),
		"HARNESS_CODEX_MODEL":                 s.config.CodexModel,
		"HARNESS_PLAYWRIGHT_WORKERS":          strconv.Itoa(s.config.PlaywrightWorkers),
		"PLAYWRIGHT_WORKERS":                  strconv.Itoa(s.config.PlaywrightWorkers),
		"PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH": "/usr/bin/chromium",
		"CI":                                  "true",
		"HARNESS_SANDBOX_REQUESTED_AT_MS":     strconv.FormatInt(requestedAt.UnixMilli(), 10),
	}
	checkpoint, checkpointKind := s.config.Checkpoint, "toolchain"
	if repositoryCheckpoint := strings.TrimSpace(s.config.RepositoryCheckpoints[repository.ID]); repositoryCheckpoint != "" {
		checkpoint, checkpointKind = repositoryCheckpoint, "repository"
	}
	variables["HARNESS_SANDBOX_CHECKPOINT"] = checkpoint
	createStarted := time.Now()
	s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID,
		Type: "run.infrastructure.stage", Level: "info", Message: "Starting Railway sandbox",
		Payload: mustJSON(map[string]any{"stage": "sandbox_create", "status": "started", "checkpoint": checkpoint, "checkpoint_kind": checkpointKind, "cache_hit": checkpointKind == "repository"})})
	instance, err := s.sandbox.Create(ctx, sandbox.CreateSpec{Checkpoint: checkpoint,
		IdleTimeoutMinutes: s.config.IdleTimeout, Variables: variables})
	if err != nil {
		s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID,
			Type: "run.infrastructure.stage", Level: "error", Message: "Railway sandbox creation failed",
			Payload: mustJSON(map[string]any{"stage": "sandbox_create", "duration_ms": time.Since(createStarted).Milliseconds(), "status": "failed", "checkpoint": checkpoint, "checkpoint_kind": checkpointKind})})
		s.releaseSlots(ctx, run.ID, slots, "")
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "quota") || strings.Contains(message, "limit") || strings.Contains(message, "capacity") {
			_ = s.store.RequeueRun(ctx, run.ID, "railway_quota")
			s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID,
				Type: "run.queued", Level: "warning", Message: "Waiting for Railway sandbox capacity"})
			return
		}
		s.pause(ctx, run, "sandbox_create_failed", err.Error())
		return
	}
	s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID, SandboxID: instance.ID,
		Type: "run.infrastructure.stage", Level: "info", Message: "Startup phase completed",
		Payload: mustJSON(map[string]any{"stage": "sandbox_create", "duration_ms": time.Since(createStarted).Milliseconds(), "status": "completed", "checkpoint": checkpoint, "checkpoint_kind": checkpointKind, "cache_hit": checkpointKind == "repository"})})
	if s.terminal(ctx, run.ID) {
		s.sandbox.Destroy(context.WithoutCancel(ctx), instance.ID)
		s.releaseSlots(ctx, run.ID, slots, "")
		return
	}
	workerStarted := time.Now()
	session, err := s.sandbox.StartWorker(ctx, instance.ID)
	if err != nil {
		s.sandbox.Destroy(context.WithoutCancel(ctx), instance.ID)
		s.releaseSlots(ctx, run.ID, slots, "")
		s.pause(ctx, run, "sandbox_worker_failed", err.Error())
		return
	}
	if err := s.store.SetSandbox(ctx, run.ID, instance.ID, session); err != nil {
		s.logger.Error("record sandbox", "run_id", run.ID, "error", err)
	}
	s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID, SandboxID: instance.ID,
		Type: "sandbox.started", Level: "info", Message: "Railway sandbox worker started",
		Payload: mustJSON(map[string]any{"session": session, "duration_ms": time.Since(workerStarted).Milliseconds(), "request_to_session_ms": time.Since(requestedAt).Milliseconds(), "checkpoint": checkpoint, "checkpoint_kind": checkpointKind})})
}

func mustJSON(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}

func (s *Scheduler) releaseSlots(ctx context.Context, runID string, slots []model.AuthSlot, reason string) {
	for _, slot := range slots {
		_ = s.store.ReleaseAuthSlot(ctx, slot.ID, runID, slot.Ciphertext, reason)
	}
}

func splitSlotIDs(value string) []string {
	var result []string
	for _, id := range strings.Split(value, ",") {
		if id = strings.TrimSpace(id); id != "" {
			result = append(result, id)
		}
	}
	return result
}

func (s *Scheduler) reconcile(ctx context.Context) {
	runs, err := s.store.ListRuns(ctx, model.RunFilter{State: "running", Limit: s.config.MaxActiveRuns * 2})
	if err != nil {
		s.logger.Error("list running runs", "error", err)
		return
	}
	for _, run := range runs {
		if run.SandboxID == "" {
			continue
		}
		if s.workerSessionStale(ctx, run) {
			s.pause(ctx, run, "sandbox_worker_start_timeout", "Sandbox session started but the worker process did not report startup within the timeout")
			continue
		}
		if err := s.sandbox.Heartbeat(ctx, run.SandboxID); err != nil {
			s.logger.Warn("sandbox heartbeat failed", "run_id", run.ID, "sandbox_id", run.SandboxID, "error", err)
			status, statusErr := s.sandbox.Status(ctx, run.SandboxID)
			if statusErr != nil || (status.State != "running" && status.State != "RUNNING") {
				for _, slotID := range splitSlotIDs(run.AuthSlotID) {
					_ = s.store.QuarantineAuthSlot(ctx, slotID, run.ID, "sandbox lost before auth return")
				}
				_ = s.store.RequeueRun(ctx, run.ID, "sandbox_lost")
				s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID, SandboxID: run.SandboxID, Type: "sandbox.lost", Level: "error", Message: "Sandbox was lost; the run will restore from its journal and pushed branch"})
			}
			continue
		}
		if err := s.store.Heartbeat(ctx, run.ID, s.config.Owner, s.config.LeaseDuration); err != nil {
			s.logger.Warn("run heartbeat failed", "run_id", run.ID, "error", err)
		}
	}
	s.cleanupTerminal(ctx)
}

func (s *Scheduler) workerSessionStale(ctx context.Context, run model.Run) bool {
	if run.CurrentStage != "" || run.SandboxID == "" {
		return false
	}
	events, err := s.store.ListEvents(ctx, model.EventFilter{RunID: run.ID, Limit: 1000})
	if err != nil {
		return false
	}
	var sessionStarted time.Time
	for _, event := range events {
		switch event.Type {
		case "worker.starting", "run.started", "run.failed", "run.paused", "pipeline.stage", "stage.started":
			return false
		case "sandbox.started":
			sessionStarted = event.CreatedAt
		}
	}
	return !sessionStarted.IsZero() && time.Since(sessionStarted) > s.config.StartupTimeout
}

func (s *Scheduler) cleanupTerminal(ctx context.Context) {
	for _, state := range []string{"completed", "awaiting_input", "paused", "cancelled"} {
		runs, err := s.store.ListRuns(ctx, model.RunFilter{State: state, Limit: 500})
		if err != nil {
			continue
		}
		for _, run := range runs {
			grace := time.Minute
			if state == "awaiting_input" {
				grace = 15 * time.Second
			}
			if run.SandboxID == "" || (state != "cancelled" && time.Since(run.UpdatedAt) < grace) {
				continue
			}
			if state == "completed" && s.previewAlive(ctx, run) {
				continue
			}
			if err := s.sandbox.Destroy(ctx, run.SandboxID); err != nil {
				s.logger.Warn("destroy terminal sandbox", "run_id", run.ID, "error", err)
				continue
			}
			if state == "cancelled" {
				for _, slotID := range splitSlotIDs(run.AuthSlotID) {
					_ = s.store.QuarantineAuthSlot(ctx, slotID, run.ID, "run cancelled before auth return")
				}
			}
			_ = s.store.SetSandbox(ctx, run.ID, "", "")
			message := "Terminal run sandbox destroyed"
			if state == "awaiting_input" {
				message = "Checkpointed input-wait sandbox destroyed"
			}
			s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID, SandboxID: run.SandboxID, Type: "sandbox.destroyed", Level: "info", Message: message})
		}
	}
}

// previewAlive keeps a completed run's sandbox running while its preview has
// not expired. Expired previews are torn down here so the sandbox destroy
// below proceeds on this same pass.
func (s *Scheduler) previewAlive(ctx context.Context, run model.Run) bool {
	switch run.PreviewState {
	case "ready":
		// The worker reported a preview but the control plane has not published
		// it yet; give publication a chance before destroying the sandbox.
		return time.Since(run.UpdatedAt) < 5*time.Minute
	case "published":
		if run.PreviewExpiresAt != nil && time.Now().Before(*run.PreviewExpiresAt) {
			if err := s.sandbox.Heartbeat(ctx, run.SandboxID); err != nil {
				s.logger.Warn("preview sandbox heartbeat failed", "run_id", run.ID, "sandbox_id", run.SandboxID, "error", err)
			}
			return true
		}
		if s.preview != nil {
			s.preview.Expire(ctx, run)
			s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID, SandboxID: run.SandboxID,
				Type: "preview.expired", Level: "info", Message: "Preview expired"})
		}
		return false
	}
	return false
}

func (s *Scheduler) pause(ctx context.Context, run model.Run, eventType, message string) {
	if err := s.store.SetRunState(ctx, run.ID, "paused", run.CurrentStage, message); err != nil {
		s.logger.Error("pause run", "run_id", run.ID, "error", err)
		return
	}
	current, err := s.store.GetRun(ctx, run.ID)
	if err != nil || current.State != "paused" {
		return
	}
	s.event(ctx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID,
		Type: eventType, Level: "error", Message: message})
}

func (s *Scheduler) terminal(ctx context.Context, runID string) bool {
	current, err := s.store.GetRun(ctx, runID)
	return err == nil && (current.State == "completed" || current.State == "cancelled")
}

func (s *Scheduler) event(ctx context.Context, event model.Event) {
	if _, err := s.store.AppendEvent(ctx, event); err != nil {
		s.logger.Error("append event", "run_id", event.RunID, "error", err)
		return
	}
	s.broker.Notify()
}
