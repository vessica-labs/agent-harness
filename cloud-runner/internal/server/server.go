package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/linear"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/preview"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/githubapp"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/linearapi"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/notionapi"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

type Server struct {
	config              Config
	store               store.Store
	box                 *secure.Box
	broker              *events.Broker
	logger              *slog.Logger
	http                *http.Server
	ready               atomic.Bool
	linearMu            sync.Mutex
	workflowMu          sync.Mutex
	previewMu           sync.RWMutex
	preview             *preview.Manager
	linear              func(string) *linearapi.Client
	githubWebhookClient func(githubapp.Credentials) githubWebhookUpdater
}

func New(config Config, values store.Store, box *secure.Box, broker *events.Broker, logger *slog.Logger) *Server {
	server := &Server{config: config, store: values, box: box, broker: broker, logger: logger, linear: linearapi.New,
		githubWebhookClient: func(credentials githubapp.Credentials) githubWebhookUpdater { return githubapp.New(credentials) }}
	server.ready.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /readyz", server.readiness)
	mux.HandleFunc("POST /webhooks/linear", server.linearWebhook)
	mux.HandleFunc("POST /webhooks/github", server.githubWebhook)
	mux.HandleFunc("GET /join", server.joinPage)
	mux.HandleFunc("POST /auth/v1/initialize", server.initializeTeam)
	mux.HandleFunc("POST /auth/v1/invitations/redeem", server.redeemInvitation)
	mux.HandleFunc("POST /auth/v1/token", server.refreshToken)
	mux.Handle("/v1/", server.management(http.HandlerFunc(server.managementRoutes)))
	mux.Handle("/internal/v1/", http.HandlerFunc(server.internalRoutes))
	mux.Handle("/previews/", http.HandlerFunc(server.previewRoutes))
	// Root-relative assets and websockets from proxied preview pages carry
	// only the preview cookie; the broker resolves them to the right target.
	mux.Handle("/", http.HandlerFunc(server.previewRoutes))
	server.http = &http.Server{Addr: config.Address, Handler: server.logging(mux),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second, WriteTimeout: 0}
	return server
}

func (s *Server) ListenAndServe() error {
	s.logger.Info("control plane listening", "address", s.config.Address)
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.ready.Store(false)
	return s.http.Shutdown(ctx)
}

func (s *Server) Handler() http.Handler { return s.http.Handler }

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "agent-harness-control-plane"})
}

func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() || s.store.Ping(r.Context()) != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) linearWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readLimited(w, r, s.config.MaxRequestBytes)
	if err != nil {
		return
	}
	secret := s.config.LinearWebhookSecret
	if secret == "" {
		if stored, err := s.credential(r.Context(), "linear_webhook_secret"); err == nil {
			secret = string(stored)
		}
	}
	if err := linear.Verify(r.Header, body, secret, time.Now(), s.config.WebhookTolerance); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	parsed, err := linear.Parse(r.Header, body, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.EqualFold(parsed.Delivery.EventType, "Comment") {
		s.linearInputComment(w, r, parsed)
		return
	}
	repository, err := s.store.FindLinearRepository(r.Context(), parsed.Delivery.WorkspaceID,
		parsed.Delivery.TeamID, parsed.Delivery.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		duplicate, _ := s.store.RecordIgnoredLinearDelivery(r.Context(), parsed.Delivery, "", "repository_not_registered")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": true, "duplicate": duplicate, "reason": "repository_not_registered"})
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	var linearClient *linearapi.Client
	if token, tokenErr := s.linearAccessToken(r.Context()); tokenErr == nil {
		linearClient = s.linear(token)
		dependencyCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if err := s.releaseLinearDependencyWaiters(dependencyCtx, repository, linearClient, parsed.Delivery.IssueKey); err != nil {
			s.logger.Error("refresh Linear dependency waiters", "issue_key", parsed.Delivery.IssueKey, "error", err)
		}
		cancel()
	}
	eligible, reason := parsed.Eligible(repository.TriggerLabel)
	if !eligible {
		duplicate, _ := s.store.RecordIgnoredLinearDelivery(r.Context(), parsed.Delivery, repository.ID, reason)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": true, "duplicate": duplicate, "reason": reason})
		return
	}
	contextValue := map[string]any{"provider": "linear", "id": parsed.Delivery.IssueID,
		"key": parsed.Delivery.IssueKey, "url": parsed.Delivery.IssueURL, "title": parsed.Delivery.IssueTitle,
		"dependencies": parsed.Delivery.Dependencies}
	if linearClient != nil {
		contextCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		issue, issueErr := linearClient.IssueContext(contextCtx, parsed.Delivery.IssueID)
		if issueErr != nil {
			issue, issueErr = linearClient.Issue(contextCtx, parsed.Delivery.IssueID)
		}
		if issueErr == nil {
			parsed.Delivery.Dependencies = linear.DependencyIssueKeys(issue.Description)
			contextValue = map[string]any{"provider": "linear", "id": issue.ID,
				"key": issue.Identifier, "url": issue.URL, "title": issue.Title, "description": issue.Description,
				"comments": issue.Comments.Nodes, "attachments": issue.Attachments.Nodes,
				"dependencies": parsed.Delivery.Dependencies}
		}
	}
	if len(parsed.Delivery.Dependencies) > 0 {
		dependencyCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		pending, dependencyErr := s.pendingLinearDependencies(dependencyCtx, linearClient, parsed.Delivery.Dependencies)
		cancel()
		contextValue["pending_dependencies"] = pending
		if dependencyErr != nil {
			parsed.Delivery.QueueReason = "dependencies_check_failed: " + strings.Join(parsed.Delivery.Dependencies, ", ")
			contextValue["dependency_check_error"] = dependencyErr.Error()
		} else if len(pending) > 0 {
			parsed.Delivery.QueueReason = "dependencies_pending: " + strings.Join(pending, ", ")
		}
	}
	parsed.Delivery.SourceContext, _ = json.Marshal(contextValue)
	result, err := s.store.AcceptLinearDelivery(r.Context(), repository, parsed.Delivery)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	if result.Run != nil && result.Duplicate {
		s.appendEvent(r.Context(), model.Event{RunID: result.Run.ID, SourceIssueID: result.Run.SourceIssueID,
			Type: "webhook.duplicate", Level: "info", Message: "Duplicate or repeated qualifying Linear webhook resolved to the existing run"})
	}
	if result.Run != nil && result.Run.State == "queued" && result.Run.CurrentStage == "" {
		event := model.Event{RunID: result.Run.ID, SourceIssueID: result.Run.SourceIssueID,
			Type: "run.queued", Level: "info", Message: "Linear issue claimed and queued"}
		if strings.HasPrefix(result.Run.QueueReason, "dependencies_") {
			event.Type, event.Message = "run.dependencies_waiting", "Run is waiting for Linear dependencies to reach Done"
			event.Payload, _ = json.Marshal(map[string]any{"dependencies": parsed.Delivery.Dependencies,
				"queue_reason": result.Run.QueueReason})
			if !result.Duplicate {
				_, _ = s.appendEvent(r.Context(), event)
			}
		}
		if err := s.syncLinearLifecycleEvent(r.Context(), result.Run.ID, event); err != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("synchronize claimed Linear issue: %w", err))
			return
		}
	}
	s.broker.Notify()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": result.Run, "duplicate": result.Duplicate})
}

func (s *Server) management(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := secure.Bearer(r.Header.Get("Authorization"))
		principal, err := s.store.AuthenticateSession(r.Context(), s.box.TokenDigest("access", token), time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusUnauthorized, errors.New("valid team session required"))
			return
		}
		if !roleAllows(principal.Member.Role, requiredRole(r)) {
			_ = s.store.AppendAuthAudit(r.Context(), model.AuthAudit{MemberID: principal.Member.ID, SessionID: principal.Session.ID, ActorID: principal.Member.ID, Action: "authorization.denied", TargetID: r.Method + " " + r.URL.Path})
			writeError(w, http.StatusForbidden, errors.New("this action is not allowed for your team role"))
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		ctx = context.WithValue(ctx, accessDigestContextKey{}, s.box.TokenDigest("access", token))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) managementRoutes(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/status":
		s.status(w, r)
	case r.URL.Path == "/v1/whoami" && r.Method == http.MethodGet:
		s.whoami(w, r)
	case r.URL.Path == "/v1/logout" && r.Method == http.MethodPost:
		s.logout(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/team/"):
		s.teamRoutes(w, r)
	case r.URL.Path == "/v1/repositories" && r.Method == http.MethodGet:
		s.listRepositories(w, r)
	case r.URL.Path == "/v1/repositories" && r.Method == http.MethodPost:
		s.putRepository(w, r)
	case r.URL.Path == "/v1/providers/linear/context" && r.Method == http.MethodGet:
		s.linearRegistrationContext(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/repositories/") && strings.Contains(r.URL.Path, "/linear/issues"):
		s.repositoryLinearIssue(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/repositories/") && r.Method == http.MethodDelete:
		id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/repositories/"), "/")
		if id == "" {
			writeError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}
		if err := s.store.DisableRepository(r.Context(), id); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "enabled": false})
	case r.URL.Path == "/v1/runs" && r.Method == http.MethodGet:
		s.listRuns(w, r)
	case r.URL.Path == "/v1/input-requests" && r.Method == http.MethodGet:
		s.listInputRequests(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/input-requests/"):
		s.inputRequestRoute(w, r)
	case r.URL.Path == "/v1/events" && r.Method == http.MethodGet:
		s.streamEvents(w, r)
	case r.URL.Path == "/v1/auth-slots":
		s.authSlots(w, r)
	case r.URL.Path == "/v1/auth/github/upgrade-webhook":
		s.upgradeGitHubWebhook(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/auth/"):
		s.auth(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/runs/"):
		s.runRoute(w, r)
	default:
		writeError(w, http.StatusNotFound, store.ErrNotFound)
	}
}

func (s *Server) repositoryLinearIssue(w http.ResponseWriter, r *http.Request) {
	segments := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/repositories/"), "/"), "/")
	if len(segments) < 3 || segments[0] == "" || segments[1] != "linear" || segments[2] != "issues" {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	repository, err := s.store.GetRepository(r.Context(), segments[0])
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !repository.Enabled {
		writeError(w, http.StatusConflict, errors.New("repository automation is disabled"))
		return
	}
	token, err := s.linearAccessToken(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	client := linearapi.New(token)
	if len(segments) == 3 && r.Method == http.MethodPost {
		var input struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		if err := decodeJSON(w, r, s.config.MaxRequestBytes, &input); err != nil {
			return
		}
		issue, err := client.CreateRootIssue(r.Context(), repository.LinearTeamID, repository.LinearProjectID,
			repository.TriggerLabel, input.Title, input.Description)
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("create Linear test issue: %w", err))
			return
		}
		writeJSON(w, http.StatusCreated, issue)
		return
	}
	if len(segments) == 5 && segments[4] == "archive" && r.Method == http.MethodPost {
		var input struct {
			Confirm bool `json:"confirm"`
		}
		if err := decodeJSON(w, r, s.config.MaxRequestBytes, &input); err != nil {
			return
		}
		if !input.Confirm {
			writeError(w, http.StatusBadRequest, errors.New("archive requires explicit confirmation"))
			return
		}
		issue, err := client.Issue(r.Context(), segments[3])
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("resolve Linear issue: %w", err))
			return
		}
		if err := s.ensureLinearIssueIsNotCanonical(r.Context(), repository.ID, issue); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		archived, err := client.ArchiveIssue(r.Context(), issue.ID)
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("archive Linear issue: %w", err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "issue": archived})
		return
	}
	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func (s *Server) ensureLinearIssueIsNotCanonical(ctx context.Context, repositoryID string, issue linearapi.Issue) error {
	runs, err := s.store.ListRuns(ctx, model.RunFilter{RepositoryID: repositoryID, Limit: 500})
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.SourceIssueID == issue.ID || run.SourceIssueKey == issue.Identifier {
			return fmt.Errorf("refusing to archive canonical source issue %s for run %s", issue.Identifier, run.ID)
		}
		tickets, listErr := s.store.ListTickets(ctx, run.ID)
		if listErr != nil {
			return listErr
		}
		for _, ticket := range tickets {
			if ticket.ProviderIssueID == issue.ID || ticket.ProviderIssueKey == issue.Identifier {
				return fmt.Errorf("refusing to archive canonical child issue %s for run %s", issue.Identifier, run.ID)
			}
		}
	}
	return nil
}

func (s *Server) linearRegistrationContext(w http.ResponseWriter, r *http.Request) {
	token, err := s.linearAccessToken(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	value, err := linearapi.New(token).RegistrationContext(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("Linear registration context: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) authSlots(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		values, err := s.store.ListAuthSlots(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"slots": values})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input struct {
		ID   string `json:"id"`
		Auth string `json:"auth"`
	}
	if err := decodeJSON(w, r, 2<<20, &input); err != nil {
		return
	}
	if input.ID == "" || input.Auth == "" {
		writeError(w, http.StatusBadRequest, errors.New("slot id and auth are required"))
		return
	}
	ciphertext, err := s.box.Seal([]byte(input.Auth), secure.Purpose("codex", input.ID))
	if err == nil {
		err = s.store.PutAuthSlot(r.Context(), model.AuthSlot{ID: input.ID, State: "available", Ciphertext: ciphertext})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": input.ID, "state": "available"})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListRuns(r.Context(), model.RunFilter{Limit: 500})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	counts := map[string]int{}
	for _, run := range runs {
		counts[run.State]++
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event_protocol": model.EventProtocol, "runs": counts})
}

func (s *Server) listRepositories(w http.ResponseWriter, r *http.Request) {
	values, err := s.store.ListRepositories(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": values})
}

func (s *Server) putRepository(w http.ResponseWriter, r *http.Request) {
	var value model.Repository
	if err := decodeJSON(w, r, s.config.MaxRequestBytes, &value); err != nil {
		return
	}
	if value.Name == "" || value.GitHubOwner == "" || value.GitHubRepo == "" ||
		value.GitHubInstallation == 0 || value.LinearWorkspaceID == "" || value.LinearTeamID == "" || value.NotionParentID == "" {
		writeError(w, http.StatusBadRequest, errors.New("name, GitHub repository and installation, Linear workspace/team, and Notion parent are required"))
		return
	}
	if err := s.validateRepositoryRegistration(r.Context(), value); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("repository registration validation: %w", err))
		return
	}
	stored, err := s.store.PutRepository(r.Context(), value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, stored)
}

func (s *Server) validateRepositoryRegistration(ctx context.Context, value model.Repository) error {
	githubRaw, err := s.credential(ctx, "github_app")
	if err != nil {
		return errors.New("GitHub App credential is not configured")
	}
	var githubCredential githubapp.Credentials
	if json.Unmarshal(githubRaw, &githubCredential) != nil || githubCredential.AppID == 0 || githubCredential.PrivateKey == "" {
		return errors.New("GitHub App credential is invalid")
	}
	if githubCredential.WebhookSecret == "" {
		return errors.New("GitHub App webhook secret is not configured; rerun the manifest flow or import GITHUB_WEBHOOK_SECRET")
	}
	if _, err := githubapp.New(githubCredential).MintInstallationToken(ctx, value.GitHubInstallation, value.GitHubOwner, value.GitHubRepo); err != nil {
		return fmt.Errorf("GitHub installation: %w", err)
	}
	linearToken, err := s.linearAccessToken(ctx)
	if err != nil {
		return err
	}
	linearClient := linearapi.New(linearToken)
	if err := linearClient.ValidateRegistration(ctx, value.LinearWorkspaceID, value.LinearTeamID, value.LinearProjectID); err != nil {
		return fmt.Errorf("Linear registration: %w", err)
	}
	if _, err := s.ensureLinearLifecycleStates(ctx, linearClient, value.LinearTeamID); err != nil {
		return fmt.Errorf("Linear workflow: %w", err)
	}
	notionToken, err := s.credential(ctx, "notion")
	if err != nil {
		return errors.New("Notion credential is not configured")
	}
	if err := notionapi.New(string(notionToken)).ValidateParent(ctx, value.NotionParentID); err != nil {
		return fmt.Errorf("Notion parent: %w", err)
	}
	return nil
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	filter := model.RunFilter{State: r.URL.Query().Get("status"), RepositoryID: r.URL.Query().Get("repo"),
		Limit: queryInt(r, "limit", 100)}
	if after := r.URL.Query().Get("after"); after != "" {
		filter.After, _ = time.Parse(time.RFC3339, after)
	}
	values, err := s.store.ListRuns(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": values})
}

func (s *Server) runRoute(w http.ResponseWriter, r *http.Request) {
	part := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
	segments := strings.Split(strings.Trim(part, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	runID := segments[0]
	if len(segments) == 1 && r.Method == http.MethodGet {
		run, err := s.store.GetRun(r.Context(), runID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		stages, stageErr := s.store.ListStages(r.Context(), runID)
		if stageErr == nil {
			stages = s.hydrateStageDefinitions(r.Context(), runID, stages)
		}
		tickets, ticketErr := s.store.ListTickets(r.Context(), runID)
		artifacts, artifactErr := s.store.ListArtifacts(r.Context(), runID)
		externalSyncs, syncErr := s.store.ListExternalSyncs(r.Context(), runID)
		inputRequests, inputErr := s.store.ListInputRequests(r.Context(), model.InputRequestFilter{RunID: runID, Limit: 100})
		inputResponses := map[string][]model.InputResponse{}
		inputDeliveries := map[string][]model.InputDelivery{}
		for _, request := range inputRequests {
			inputResponses[request.ID], _ = s.store.ListInputResponses(r.Context(), request.ID)
			inputDeliveries[request.ID], _ = s.store.ListInputDeliveries(r.Context(), request.ID)
		}
		if stageErr != nil || ticketErr != nil || artifactErr != nil || syncErr != nil || inputErr != nil {
			writeError(w, http.StatusInternalServerError, errors.Join(stageErr, ticketErr, artifactErr, syncErr, inputErr))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"run": run, "stages": stages, "tickets": tickets,
			"artifacts": artifacts, "external_syncs": externalSyncs, "input_requests": inputRequests,
			"input_responses": inputResponses, "input_deliveries": inputDeliveries})
		return
	}
	if len(segments) != 2 {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	switch segments[1] {
	case "artifacts":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if artifactPath := r.URL.Query().Get("path"); artifactPath != "" {
			artifact, err := s.store.GetArtifact(r.Context(), runID, artifactPath)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			w.Header().Set("Content-Type", artifact.MediaType)
			w.Header().Set("ETag", `"`+artifact.SHA256+`"`)
			w.Write(artifact.Content)
			return
		}
		values, err := s.store.ListArtifacts(r.Context(), runID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifacts": values})
	case "input":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		var input struct {
			FeatureRequest string `json:"feature_request"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid run input"))
			return
		}
		input.FeatureRequest = strings.TrimSpace(input.FeatureRequest)
		if input.FeatureRequest == "" {
			writeError(w, http.StatusBadRequest, errors.New("feature_request is required"))
			return
		}
		if err := s.store.UpdateRunInput(r.Context(), runID, input.FeatureRequest); err != nil {
			writeStoreError(w, err)
			return
		}
		s.appendEvent(r.Context(), model.Event{RunID: runID, Type: "run.input_updated", Level: "info", Message: "Operator feedback updated the run input"})
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	case "resume":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if err := s.store.ResumeRun(r.Context(), runID); err != nil {
			writeStoreError(w, err)
			return
		}
		s.appendEvent(r.Context(), model.Event{RunID: runID, Type: "run.resumed", Level: "info", Message: "Run queued for recovery"})
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	case "cancel":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if err := s.store.CancelRun(r.Context(), runID); err != nil {
			writeStoreError(w, err)
			return
		}
		s.appendEvent(r.Context(), model.Event{RunID: runID, Type: "run.cancelled", Level: "warning", Message: "Run cancelled by operator"})
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	case "reconcile":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		result, err := s.reconcileRunProjections(r.Context(), runID)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		s.appendEvent(r.Context(), model.Event{RunID: runID, Type: "external.reconciled", Level: "info", Message: "Linear and Notion projections reconciled"})
		writeJSON(w, http.StatusOK, result)
	default:
		writeError(w, http.StatusNotFound, store.ErrNotFound)
	}
}

func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/auth/"), "/")
	if name == "" || strings.Contains(name, "/") {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	if r.Method == http.MethodGet {
		_, err := s.store.GetCredential(r.Context(), name)
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "configured": err == nil})
		return
	}
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(w, r, 1<<20, &input); err != nil {
		return
	}
	if input.Value == "" {
		writeError(w, http.StatusBadRequest, errors.New("credential value is required"))
		return
	}
	ciphertext, err := s.box.Seal([]byte(input.Value), secure.Purpose("credential", name))
	if err == nil {
		err = s.store.PutCredential(r.Context(), model.Credential{Name: name, Ciphertext: ciphertext})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "configured": true})
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	after := int64(queryInt(r, "after", 0))
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		if parsed, err := strconv.ParseInt(header, 10, 64); err == nil && parsed > after {
			after = parsed
		}
	}
	runID := r.URL.Query().Get("run_id")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	// A short retry keeps expected access-token rotation unobtrusive while
	// Last-Event-ID ensures the browser resumes without dropping events.
	fmt.Fprint(w, "retry: 1000\n: connected\n\n")
	flusher.Flush()
	updates, unsubscribe := s.broker.Subscribe()
	defer unsubscribe()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		events, err := s.store.ListEvents(r.Context(), model.EventFilter{After: after, RunID: runID, Limit: 250})
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: {\"message\":\"event store unavailable\"}\n\n")
			flusher.Flush()
			return
		}
		for _, event := range events {
			body, _ := json.Marshal(event)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.GlobalSeq, body)
			after = event.GlobalSeq
		}
		if len(events) > 0 {
			flusher.Flush()
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-updates:
		case <-keepalive.C:
			digest, _ := r.Context().Value(accessDigestContextKey{}).([]byte)
			if _, err := s.store.AuthenticateSession(r.Context(), digest, time.Now().UTC()); err != nil {
				fmt.Fprint(w, "event: auth_revoked\ndata: {\"message\":\"team session expired or revoked\"}\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) internalRoutes(w http.ResponseWriter, r *http.Request) {
	part := strings.TrimPrefix(r.URL.Path, "/internal/v1/runs/")
	segments := strings.Split(strings.Trim(part, "/"), "/")
	if len(segments) != 2 {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	runID, action := segments[0], segments[1]
	if err := s.box.VerifyCapability(secure.Bearer(r.Header.Get("Authorization")), runID, time.Now()); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	switch action {
	case "events":
		s.internalEvent(w, r, runID)
	case "journal":
		s.internalJournal(w, r, runID)
	case "heartbeat":
		s.internalHeartbeat(w, r, runID)
	case "auth-return":
		s.internalAuthReturn(w, r, runID)
	case "github-token":
		s.internalGitHubToken(w, r, runID)
	case "worker-binary":
		s.internalWorkerBinary(w, r)
	case "sync":
		s.internalSync(w, r, runID)
	default:
		writeError(w, http.StatusNotFound, store.ErrNotFound)
	}
}

func (s *Server) internalWorkerBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	executable, err := os.Executable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	file, err := os.Open(executable)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Agent-Harness-Worker-SHA256", hex.EncodeToString(hash.Sum(nil)))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "agent-harness", info.ModTime(), file)
}

func (s *Server) internalGitHubToken(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	repository, err := s.store.GetRepository(r.Context(), run.RepositoryID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if repository.GitHubInstallation == 0 {
		writeError(w, http.StatusConflict, errors.New("repository has no GitHub App installation"))
		return
	}
	raw, err := s.credential(r.Context(), "github_app")
	if err != nil {
		writeError(w, http.StatusConflict, errors.New("GitHub App credentials are not configured"))
		return
	}
	var credentials githubapp.Credentials
	if err := json.Unmarshal(raw, &credentials); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("stored GitHub App credentials are invalid"))
		return
	}
	token, err := githubapp.New(credentials).MintInstallationToken(r.Context(), repository.GitHubInstallation,
		repository.GitHubOwner, repository.GitHubRepo)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, token)
}

func (s *Server) internalEvent(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var value model.Event
	if err := decodeJSON(w, r, s.config.MaxRequestBytes, &value); err != nil {
		return
	}
	value.RunID = runID
	capability := secure.Bearer(r.Header.Get("Authorization"))
	value.Message = secure.Redact(value.Message, s.config.ManagementToken, capability)
	value.Payload = secure.RedactJSON(value.Payload, s.config.ManagementToken, capability)
	var inputRequest *model.InputRequest
	if value.Type == "human_input.requested" {
		request, requestErr := decodeInputRequestEvent(runID, value.Stage, value.Payload)
		if requestErr != nil {
			writeError(w, http.StatusBadRequest, requestErr)
			return
		}
		request, requestErr = s.store.CreateInputRequest(r.Context(), request)
		if requestErr != nil {
			writeStoreError(w, requestErr)
			return
		}
		inputRequest = &request
	}
	stored, err := s.appendEvent(r.Context(), value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if value.Type == "codex.usage" {
		var usage model.Usage
		if json.Unmarshal(value.Payload, &usage) != nil || usage.Model == "" || usage.InputTokens < 0 || usage.CachedInputTokens < 0 || usage.OutputTokens < 0 {
			writeError(w, http.StatusBadRequest, errors.New("invalid Codex usage payload"))
			return
		}
		if err := s.store.AddRunUsage(r.Context(), runID, usage); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if value.Type == "human_input.requested" && inputRequest != nil {
		_ = s.store.PutStage(r.Context(), model.StageState{RunID: runID, Stage: value.Stage,
			State: "waiting_for_input", Details: value.Payload, StartedAt: &stored.CreatedAt})
		if err := s.syncLinearInputRequested(r.Context(), *inputRequest); err != nil {
			s.logger.Error("project human input request to Linear", "run_id", runID, "request_id", inputRequest.ID, "error", err)
		}
	} else if value.Type == "run.completed" {
		_ = s.store.SetRunState(r.Context(), runID, "completed", value.Stage, "")
		go s.publishPreview(runID)
	} else if value.Type == "run.paused" || value.Type == "run.failed" {
		_ = s.store.SetRunState(r.Context(), runID, "paused", value.Stage, value.Message)
	} else if value.Type == "pipeline.stage" || value.Type == "stage.started" || value.Type == "stage.completed" || value.Type == "stage.retrying" {
		state := map[string]string{"pipeline.stage": "pending", "stage.started": "running", "stage.completed": "completed", "stage.retrying": "pending"}[value.Type]
		details := value.Payload
		if value.Type != "pipeline.stage" {
			details = s.mergeStageDetails(r.Context(), runID, value.Stage, value.Payload)
		}
		stage := model.StageState{RunID: runID, Stage: value.Stage, State: state, Details: details}
		if value.Type == "stage.started" {
			stage.StartedAt = &stored.CreatedAt
			_ = s.store.SetRunState(r.Context(), runID, "running", value.Stage, "")
		}
		if value.Type == "stage.completed" {
			stage.CompletedAt = &stored.CreatedAt
		}
		_ = s.store.PutStage(r.Context(), stage)
	} else if value.Type == "ticket.started" || value.Type == "ticket.completed" || value.Type == "ticket.failed" {
		var payload struct {
			Key          string   `json:"ticket_key"`
			Commit       string   `json:"commit"`
			Dependencies []string `json:"depends_on"`
			Owner        string   `json:"owner"`
		}
		if json.Unmarshal(value.Payload, &payload) == nil && payload.Key != "" {
			state := map[string]string{"ticket.started": "running", "ticket.completed": "completed", "ticket.failed": "failed"}[value.Type]
			_ = s.store.PutTicket(r.Context(), model.TicketState{RunID: runID, LogicalKey: payload.Key,
				State: state, Owner: payload.Owner, CommitSHA: payload.Commit, Dependencies: payload.Dependencies, Metadata: value.Payload})
		}
	} else if value.Type == "pr.created" {
		var payload struct {
			Branch string `json:"branch"`
			URL    string `json:"url"`
		}
		if json.Unmarshal(value.Payload, &payload) == nil {
			_ = s.store.SetDelivery(r.Context(), runID, payload.Branch, payload.URL)
		}
	} else if value.Type == "preview.ready" {
		var payload struct {
			Port int `json:"port"`
		}
		if json.Unmarshal(value.Payload, &payload) == nil && payload.Port > 0 {
			_ = s.store.SetPreview(r.Context(), runID, "ready", "", payload.Port, nil)
		}
	}
	if value.Type == "stage.started" || value.Type == "stage.completed" || value.Type == "stage.retrying" ||
		value.Type == "run.completed" {
		if err := s.syncLinearLifecycleEvent(r.Context(), runID, stored); err != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("synchronize Linear lifecycle: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusCreated, stored)
}

func (s *Server) mergeStageDetails(ctx context.Context, runID, stage string, incoming json.RawMessage) json.RawMessage {
	merged := map[string]any{}
	if values, err := s.store.ListStages(ctx, runID); err == nil {
		for _, value := range values {
			if value.Stage == stage {
				_ = json.Unmarshal(value.Details, &merged)
				break
			}
		}
	}
	var update map[string]any
	if json.Unmarshal(incoming, &update) == nil {
		for key, value := range update {
			merged[key] = value
		}
	}
	result, _ := json.Marshal(merged)
	return result
}

func (s *Server) hydrateStageDefinitions(ctx context.Context, runID string, stages []model.StageState) []model.StageState {
	events, err := s.store.ListEvents(ctx, model.EventFilter{RunID: runID, Limit: 1000})
	if err != nil {
		return stages
	}
	definitions := map[string]json.RawMessage{}
	order := 0
	for _, event := range events {
		if event.Type == "pipeline.stage" && event.Stage != "" {
			var definition map[string]any
			_ = json.Unmarshal(event.Payload, &definition)
			if _, exists := definition["order"]; !exists {
				definition["order"] = order
			}
			definitions[event.Stage], _ = json.Marshal(definition)
			order++
		}
	}
	for index := range stages {
		definition, ok := definitions[stages[index].Stage]
		if !ok {
			continue
		}
		var base, current map[string]any
		_ = json.Unmarshal(definition, &base)
		_ = json.Unmarshal(stages[index].Details, &current)
		for key, value := range current {
			base[key] = value
		}
		stages[index].Details, _ = json.Marshal(base)
	}
	return stages
}

func (s *Server) internalJournal(w http.ResponseWriter, r *http.Request, runID string) {
	const journalPath = "journal/run.tar.gz"
	if r.Method == http.MethodGet {
		value, err := s.store.GetArtifact(r.Context(), runID, journalPath)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.Header().Set("Content-Type", value.MediaType)
		w.Header().Set("ETag", `"`+value.SHA256+`"`)
		w.Write(value.Content)
		return
	}
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	reader := http.MaxBytesReader(w, r.Body, s.config.MaxJournalBytes)
	body, err := io.ReadAll(reader)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("journal exceeds configured limit"))
		return
	}
	hash := sha256.Sum256(body)
	artifact := model.Artifact{RunID: runID, Path: journalPath, MediaType: "application/gzip",
		SHA256: hex.EncodeToString(hash[:]), Size: int64(len(body)), Content: body}
	if err := s.store.PutArtifact(r.Context(), artifact); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sha256": artifact.SHA256, "size": artifact.Size})
}

func (s *Server) internalHeartbeat(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input struct {
		Owner string `json:"owner"`
	}
	if err := decodeJSON(w, r, 1024, &input); err != nil {
		return
	}
	if err := s.store.Heartbeat(r.Context(), runID, input.Owner, s.config.InternalLeaseDuration); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) internalAuthReturn(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input struct {
		SlotID string `json:"slot_id"`
		Auth   string `json:"auth"`
		Error  string `json:"error"`
	}
	if err := decodeJSON(w, r, 2<<20, &input); err != nil {
		return
	}
	ciphertext, err := s.box.Seal([]byte(input.Auth), secure.Purpose("codex", input.SlotID))
	if err == nil {
		err = s.store.ReleaseAuthSlot(r.Context(), input.SlotID, runID, ciphertext, input.Error)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) appendEvent(ctx context.Context, value model.Event) (model.Event, error) {
	stored, err := s.store.AppendEvent(ctx, value)
	if err == nil {
		s.broker.Notify()
	}
	return stored, err
}

func (s *Server) credential(ctx context.Context, name string) ([]byte, error) {
	value, err := s.store.GetCredential(ctx, name)
	if err != nil {
		return nil, err
	}
	return s.box.Open(value.Ciphertext, secure.Purpose("credential", name))
}

func (s *Server) putCredential(ctx context.Context, name string, plaintext []byte) error {
	ciphertext, err := s.box.Seal(plaintext, secure.Purpose("credential", name))
	if err != nil {
		return err
	}
	return s.store.PutCredential(ctx, model.Credential{Name: name, Ciphertext: ciphertext})
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			s.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}
	})
}

func readLimited(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("request body exceeds configured limit"))
		return nil, err
	}
	return body, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, destination any) error {
	body, err := readLimited(w, r, limit)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return err
	}
	return nil
}

func queryInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return value
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var _ = path.Clean
