package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

type Memory struct {
	mu           sync.Mutex
	repositories map[string]model.Repository
	deliveries   map[string]model.DeliveryResult
	claims       map[string]string
	runs         map[string]model.Run
	events       []model.Event
	artifacts    map[string]model.Artifact
	credentials  map[string]model.Credential
	authSlots    map[string]model.AuthSlot
	externalSync map[string]model.ExternalSync
	stages       map[string]model.StageState
	tickets      map[string]model.TicketState
	seq          int64
}

func NewMemory() *Memory {
	return &Memory{
		repositories: make(map[string]model.Repository),
		deliveries:   make(map[string]model.DeliveryResult), claims: make(map[string]string),
		runs: make(map[string]model.Run), artifacts: make(map[string]model.Artifact),
		credentials: make(map[string]model.Credential), authSlots: make(map[string]model.AuthSlot),
		externalSync: make(map[string]model.ExternalSync),
		stages:       make(map[string]model.StageState), tickets: make(map[string]model.TicketState),
	}
}

func (m *Memory) PutStage(_ context.Context, value model.StageState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.stages[value.RunID+":"+value.Stage]; ok {
		if value.StartedAt == nil {
			value.StartedAt = previous.StartedAt
		}
	}
	value.UpdatedAt = time.Now().UTC()
	m.stages[value.RunID+":"+value.Stage] = value
	return nil
}

func (m *Memory) ListStages(_ context.Context, runID string) ([]model.StageState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]model.StageState, 0)
	for _, value := range m.stages {
		if value.RunID == runID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Stage < result[j].Stage })
	return result, nil
}

func (m *Memory) PutTicket(_ context.Context, value model.TicketState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.tickets[value.RunID+":"+value.LogicalKey]; ok {
		if value.ProviderIssueID == "" {
			value.ProviderIssueID = previous.ProviderIssueID
		}
		if value.ProviderIssueKey == "" {
			value.ProviderIssueKey = previous.ProviderIssueKey
		}
		if value.CommitSHA == "" {
			value.CommitSHA = previous.CommitSHA
		}
	}
	value.UpdatedAt = time.Now().UTC()
	m.tickets[value.RunID+":"+value.LogicalKey] = value
	return nil
}

func (m *Memory) ListTickets(_ context.Context, runID string) ([]model.TicketState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]model.TicketState, 0)
	for _, value := range m.tickets {
		if value.RunID == runID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LogicalKey < result[j].LogicalKey })
	return result, nil
}

func (m *Memory) Ping(context.Context) error    { return nil }
func (m *Memory) Close()                        {}
func (m *Memory) Migrate(context.Context) error { return nil }

func (m *Memory) PutRepository(_ context.Context, value model.Repository) (model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value.ID == "" {
		value.ID = newID("repo")
	}
	if value.BaseBranch == "" {
		value.BaseBranch = "main"
	}
	if value.TriggerLabel == "" {
		value.TriggerLabel = "agent-harness"
	}
	now := time.Now().UTC()
	if previous, ok := m.repositories[value.ID]; ok {
		value.CreatedAt = previous.CreatedAt
	} else {
		value.CreatedAt = now
	}
	value.UpdatedAt = now
	m.repositories[value.ID] = value
	return value, nil
}

func (m *Memory) ListRepositories(context.Context) ([]model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]model.Repository, 0, len(m.repositories))
	for _, value := range m.repositories {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (m *Memory) GetRepository(_ context.Context, id string) (model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.repositories[id]
	if !ok {
		return value, ErrNotFound
	}
	return value, nil
}

func (m *Memory) DisableRepository(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.repositories[id]
	if !ok {
		return ErrNotFound
	}
	value.Enabled = false
	value.UpdatedAt = time.Now().UTC()
	m.repositories[id] = value
	return nil
}

func (m *Memory) FindLinearRepository(_ context.Context, workspaceID, teamID, projectID string) (model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, value := range m.repositories {
		if value.Enabled && value.LinearWorkspaceID == workspaceID && value.LinearTeamID == teamID &&
			(value.LinearProjectID == "" || value.LinearProjectID == projectID) {
			return value, nil
		}
	}
	return model.Repository{}, ErrNotFound
}

func (m *Memory) AcceptLinearDelivery(_ context.Context, repo model.Repository, delivery model.LinearDelivery) (model.DeliveryResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deliveryKey := "linear:" + delivery.DeliveryID
	if result, ok := m.deliveries[deliveryKey]; ok {
		result.Duplicate = true
		if result.Run != nil {
			value := m.runs[result.Run.ID]
			result.Run = &value
		}
		return result, nil
	}
	claimKey := "linear:" + delivery.IssueID
	if runID, ok := m.claims[claimKey]; ok {
		value := m.runs[runID]
		result := model.DeliveryResult{Run: &value, Duplicate: true}
		m.deliveries[deliveryKey] = result
		return result, nil
	}
	now := delivery.ReceivedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	value := model.Run{ID: newID("run"), RepositoryID: repo.ID, Provider: "linear",
		SourceIssueID: delivery.IssueID, SourceIssueKey: delivery.IssueKey,
		SourceIssueURL: delivery.IssueURL, SourceIssueTitle: delivery.IssueTitle,
		FeatureRequest: delivery.FeatureRequest, State: "queued", CreatedAt: now, UpdatedAt: now}
	m.runs[value.ID] = value
	m.claims[claimKey] = value.ID
	m.appendEventLocked(model.Event{RunID: value.ID, SourceIssueID: value.SourceIssueID,
		Type: "run.queued", Level: "info", Message: "Linear issue claimed and queued"})
	result := model.DeliveryResult{Run: &value}
	m.deliveries[deliveryKey] = result
	return result, nil
}

func (m *Memory) RecordIgnoredLinearDelivery(_ context.Context, delivery model.LinearDelivery, repositoryID, reason string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := "linear:" + delivery.DeliveryID
	if _, ok := m.deliveries[key]; ok {
		return true, nil
	}
	m.deliveries[key] = model.DeliveryResult{Ignored: true, Reason: reason}
	_ = repositoryID
	return false, nil
}

func (m *Memory) ClaimNextRun(_ context.Context, owner string, maxActive int, lease time.Duration) (model.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	active := 0
	for _, value := range m.runs {
		if value.State == "running" && value.LeaseExpiresAt != nil && value.LeaseExpiresAt.After(now) {
			active++
		}
	}
	if active >= maxActive {
		for id, value := range m.runs {
			if value.State == "queued" {
				value.QueueReason, value.UpdatedAt = "concurrency_limit", now
				m.runs[id] = value
			}
		}
		return model.Run{}, ErrNoRunnableRun
	}
	var candidates []model.Run
	for _, value := range m.runs {
		if value.State == "queued" || (value.State == "running" && value.LeaseExpiresAt != nil && value.LeaseExpiresAt.Before(now)) {
			candidates = append(candidates, value)
		}
	}
	if len(candidates) == 0 {
		return model.Run{}, ErrNoRunnableRun
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.Before(candidates[j].CreatedAt) })
	value := candidates[0]
	expires := now.Add(lease)
	value.State, value.LeaseOwner, value.LeaseExpiresAt, value.HeartbeatAt = "running", owner, &expires, &now
	value.QueueReason = ""
	value.Attempt++
	value.UpdatedAt = now
	m.runs[value.ID] = value
	m.appendEventLocked(model.Event{RunID: value.ID, SourceIssueID: value.SourceIssueID,
		Type: "run.claimed", Level: "info", Message: "Run leased by dispatcher"})
	return value, nil
}

func (m *Memory) GetRun(_ context.Context, id string) (model.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok {
		return value, ErrNotFound
	}
	return value, nil
}

func (m *Memory) ListRuns(_ context.Context, filter model.RunFilter) ([]model.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]model.Run, 0)
	for _, value := range m.runs {
		if filter.State != "" && value.State != filter.State {
			continue
		}
		if filter.RepositoryID != "" && value.RepositoryID != filter.RepositoryID {
			continue
		}
		if !filter.After.IsZero() && !value.UpdatedAt.After(filter.After) {
			continue
		}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	limit := filter.Limit
	if limit <= 0 || limit > len(result) {
		limit = len(result)
	}
	return result[:limit], nil
}

func (m *Memory) SetRunState(_ context.Context, id, state, stage, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok {
		return ErrNotFound
	}
	if (value.State == "completed" || value.State == "cancelled") && value.State != state {
		return nil
	}
	now := time.Now().UTC()
	value.State, value.CurrentStage, value.Error, value.UpdatedAt = state, stage, message, now
	if state == "completed" || state == "cancelled" {
		value.CompletedAt = &now
	}
	m.runs[id] = value
	return nil
}

func (m *Memory) RequeueRun(_ context.Context, id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok {
		return ErrNotFound
	}
	if value.State == "completed" || value.State == "cancelled" {
		return nil
	}
	value.State, value.QueueReason, value.LeaseOwner, value.LeaseExpiresAt = "queued", reason, "", nil
	value.UpdatedAt = time.Now().UTC()
	m.runs[id] = value
	return nil
}

func (m *Memory) SetSandbox(_ context.Context, id, sandboxID, session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok {
		return ErrNotFound
	}
	value.SandboxID, value.SandboxSession, value.UpdatedAt = sandboxID, session, time.Now().UTC()
	m.runs[id] = value
	return nil
}

func (m *Memory) SetAuthSlot(_ context.Context, id, slotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok {
		return ErrNotFound
	}
	value.AuthSlotID, value.UpdatedAt = slotID, time.Now().UTC()
	m.runs[id] = value
	return nil
}

func (m *Memory) SetDelivery(_ context.Context, id, branch, pullRequestURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok {
		return ErrNotFound
	}
	value.Branch, value.PullRequestURL, value.UpdatedAt = branch, pullRequestURL, time.Now().UTC()
	m.runs[id] = value
	return nil
}

func (m *Memory) Heartbeat(_ context.Context, id, owner string, lease time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok || value.LeaseOwner != owner || value.State != "running" {
		return ErrNotFound
	}
	now, expires := time.Now().UTC(), time.Now().UTC().Add(lease)
	value.HeartbeatAt, value.LeaseExpiresAt, value.UpdatedAt = &now, &expires, now
	m.runs[id] = value
	return nil
}

func (m *Memory) UpdateRunInput(_ context.Context, id, featureRequest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok {
		return ErrNotFound
	}
	if value.State != "paused" {
		return ErrConflict
	}
	value.FeatureRequest, value.Error, value.UpdatedAt = featureRequest, "", time.Now().UTC()
	m.runs[id] = value
	return nil
}

func (m *Memory) ResumeRun(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok || value.State != "paused" {
		return ErrNotFound
	}
	value.State, value.Error, value.QueueReason, value.LeaseOwner = "queued", "", "", ""
	value.LeaseExpiresAt, value.UpdatedAt = nil, time.Now().UTC()
	m.runs[id] = value
	return nil
}

func (m *Memory) CancelRun(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.runs[id]
	if !ok || value.State == "completed" || value.State == "cancelled" {
		return ErrNotFound
	}
	now := time.Now().UTC()
	value.State, value.LeaseOwner, value.LeaseExpiresAt, value.CompletedAt, value.UpdatedAt = "cancelled", "", nil, &now, now
	m.runs[id] = value
	return nil
}

func (m *Memory) appendEventLocked(value model.Event) model.Event {
	m.seq++
	value.ID, value.GlobalSeq, value.Protocol = m.seq, m.seq, model.EventProtocol
	for _, event := range m.events {
		if event.RunID == value.RunID && event.RunSeq >= value.RunSeq {
			value.RunSeq = event.RunSeq + 1
		}
	}
	if value.RunSeq == 0 {
		value.RunSeq = 1
	}
	if value.Level == "" {
		value.Level = "info"
	}
	value.CreatedAt = time.Now().UTC()
	m.events = append(m.events, value)
	return value
}

func (m *Memory) AppendEvent(_ context.Context, value model.Event) (model.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[value.RunID]; !ok {
		return value, ErrNotFound
	}
	return m.appendEventLocked(value), nil
}

func (m *Memory) ListEvents(_ context.Context, filter model.EventFilter) ([]model.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]model.Event, 0)
	for _, value := range m.events {
		if value.GlobalSeq <= filter.After || (filter.RunID != "" && value.RunID != filter.RunID) {
			continue
		}
		result = append(result, value)
		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}
	return result, nil
}

func artifactKey(runID, path string) string { return runID + "\x00" + path }

func (m *Memory) PutArtifact(_ context.Context, value model.Artifact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	if previous, ok := m.artifacts[artifactKey(value.RunID, value.Path)]; ok {
		value.CreatedAt = previous.CreatedAt
	} else {
		value.CreatedAt = now
	}
	value.UpdatedAt = now
	value.Content = append([]byte(nil), value.Content...)
	m.artifacts[artifactKey(value.RunID, value.Path)] = value
	return nil
}

func (m *Memory) GetArtifact(_ context.Context, runID, path string) (model.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.artifacts[artifactKey(runID, path)]
	if !ok {
		return value, ErrNotFound
	}
	value.Content = append([]byte(nil), value.Content...)
	return value, nil
}

func (m *Memory) ListArtifacts(_ context.Context, runID string) ([]model.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]model.Artifact, 0)
	for _, value := range m.artifacts {
		if value.RunID == runID {
			value.Content = nil
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func syncKey(runID, logicalKey, provider string) string {
	return runID + "\x00" + logicalKey + "\x00" + provider
}

func (m *Memory) GetExternalSync(_ context.Context, runID, logicalKey, provider string) (model.ExternalSync, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.externalSync[syncKey(runID, logicalKey, provider)]
	if !ok {
		return value, ErrNotFound
	}
	return value, nil
}

func (m *Memory) ListExternalSyncs(_ context.Context, runID string) ([]model.ExternalSync, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]model.ExternalSync, 0)
	for _, value := range m.externalSync {
		if value.RunID == runID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider == result[j].Provider {
			return result[i].LogicalKey < result[j].LogicalKey
		}
		return result[i].Provider < result[j].Provider
	})
	return result, nil
}

func (m *Memory) PutExternalSync(_ context.Context, value model.ExternalSync) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value.UpdatedAt = time.Now().UTC()
	m.externalSync[syncKey(value.RunID, value.LogicalKey, value.Provider)] = value
	return nil
}

func (m *Memory) PutCredential(_ context.Context, value model.Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value.UpdatedAt = time.Now().UTC()
	value.Ciphertext = append([]byte(nil), value.Ciphertext...)
	m.credentials[value.Name] = value
	return nil
}

func (m *Memory) GetCredential(_ context.Context, name string) (model.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.credentials[name]
	if !ok {
		return value, ErrNotFound
	}
	value.Ciphertext = append([]byte(nil), value.Ciphertext...)
	return value, nil
}

func (m *Memory) PutAuthSlot(_ context.Context, value model.AuthSlot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.authSlots[value.ID]; ok && existing.State == "leased" {
		return ErrConflict
	}
	value.UpdatedAt = time.Now().UTC()
	value.Ciphertext = append([]byte(nil), value.Ciphertext...)
	m.authSlots[value.ID] = value
	return nil
}

func (m *Memory) ListAuthSlots(context.Context) ([]model.AuthSlot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]model.AuthSlot, 0, len(m.authSlots))
	for _, value := range m.authSlots {
		value.Ciphertext = nil
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (m *Memory) LeaseAuthSlots(_ context.Context, runID string, count int, lease time.Duration) ([]model.AuthSlot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if count < 1 {
		return nil, ErrNoAuthSlot
	}
	now := time.Now().UTC()
	var ids []string
	for id, value := range m.authSlots {
		if value.State == "available" || (value.State == "leased" && value.LeaseExpiresAt != nil && value.LeaseExpiresAt.Before(now)) {
			ids = append(ids, id)
		}
	}
	if len(ids) < count {
		return nil, ErrNoAuthSlot
	}
	sort.Strings(ids)
	expires := now.Add(lease)
	result := make([]model.AuthSlot, 0, count)
	for _, id := range ids[:count] {
		value := m.authSlots[id]
		value.State, value.LeaseRunID, value.LeaseExpiresAt, value.UpdatedAt = "leased", runID, &expires, now
		m.authSlots[value.ID] = value
		result = append(result, value)
	}
	return result, nil
}

func (m *Memory) ReleaseAuthSlot(_ context.Context, id, runID string, ciphertext []byte, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.authSlots[id]
	if !ok || value.LeaseRunID != runID {
		return ErrNotFound
	}
	value.State = "available"
	if lastError != "" {
		value.State = "quarantined"
	}
	value.Ciphertext, value.LastError, value.LeaseRunID, value.LeaseExpiresAt = append([]byte(nil), ciphertext...), lastError, "", nil
	value.UpdatedAt = time.Now().UTC()
	m.authSlots[id] = value
	return nil
}

func (m *Memory) QuarantineAuthSlot(_ context.Context, id, runID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.authSlots[id]
	if !ok || value.LeaseRunID != runID {
		return ErrNotFound
	}
	value.State, value.LeaseRunID, value.LeaseExpiresAt, value.LastError = "quarantined", "", nil, reason
	value.UpdatedAt = time.Now().UTC()
	m.authSlots[id] = value
	return nil
}
