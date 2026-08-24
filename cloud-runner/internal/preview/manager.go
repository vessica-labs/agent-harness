package preview

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/sandbox"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

// Manager owns the preview lifecycle: forwarding a completed run's sandbox
// application to a loopback port, minting the capability link, persisting
// durable preview state, and tearing everything down at expiry.
type Manager struct {
	Store     store.Store
	Forwarder sandbox.Forwarder
	Broker    *Broker
	PublicURL string
	TTL       time.Duration
	MaxAge    time.Duration
	Logger    *slog.Logger

	publishMu    sync.Mutex
	publishLocks map[string]*publicationLock
	mu           sync.Mutex
	lastPersist  map[string]time.Time
}

type publicationLock struct {
	mu    sync.Mutex
	users int
}

func NewManager(st store.Store, forwarder sandbox.Forwarder, broker *Broker, publicURL string, ttl, maxAge time.Duration, logger *slog.Logger) *Manager {
	if ttl <= 0 {
		ttl = time.Hour
	}
	if maxAge <= 0 {
		maxAge = 4 * time.Hour
	}
	manager := &Manager{Store: st, Forwarder: forwarder, Broker: broker,
		PublicURL: strings.TrimRight(strings.TrimSpace(publicURL), "/"),
		TTL:       ttl, MaxAge: maxAge, Logger: logger, publishLocks: map[string]*publicationLock{}, lastPersist: map[string]time.Time{}}
	broker.SetActivityCallback(manager.recordActivity)
	return manager
}

// Enabled reports whether the control plane can publish previews at all.
func (m *Manager) Enabled() bool {
	return m != nil && m.Forwarder != nil && m.PublicURL != ""
}

// Publish forwards the sandbox preview port, mints a capability link, and
// persists the published preview on the run. The run must already carry the
// preview port reported by the worker.
func (m *Manager) Publish(ctx context.Context, run model.Run) (string, error) {
	unlock := m.lockPublication(run.ID)
	defer unlock()

	if !m.Enabled() {
		return "", fmt.Errorf("previews are not configured on this control plane")
	}
	stored, err := m.Store.GetRun(ctx, run.ID)
	if err != nil {
		return "", err
	}
	if stored.PreviewState == "published" && stored.PreviewURL != "" && m.Broker.Registered(run.ID) {
		return stored.PreviewURL, nil
	}
	run = stored
	if run.SandboxID == "" || run.PreviewPort <= 0 {
		return "", fmt.Errorf("run has no sandbox preview to publish")
	}
	localURL, stop, err := m.Forwarder.Forward(ctx, run.SandboxID, run.PreviewPort)
	if err != nil {
		return "", err
	}
	if err := m.Broker.Register(run.ID, localURL, stop); err != nil {
		stop()
		return "", err
	}
	deadline := time.Now().Add(m.MaxAge)
	token, expires, err := m.Broker.Issue(run.ID, deadline)
	if err != nil {
		m.Broker.Remove(run.ID)
		return "", err
	}
	publicURL, err := m.publicPreviewURL(run.ID, token)
	if err != nil {
		m.Broker.Remove(run.ID)
		return "", err
	}
	expiresAt := expires.UTC()
	if err := m.Store.SetPreview(ctx, run.ID, "published", publicURL, run.PreviewPort, &expiresAt); err != nil {
		m.Broker.Remove(run.ID)
		return "", err
	}
	return publicURL, nil
}

// RecordFailure projects a publication failure without overwriting a preview
// that a concurrent trigger already published successfully.
func (m *Manager) RecordFailure(ctx context.Context, runID string, port int) (bool, error) {
	unlock := m.lockPublication(runID)
	defer unlock()

	stored, err := m.Store.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	if stored.PreviewState == "published" && m.Broker.Registered(runID) {
		return false, nil
	}
	if stored.PreviewPort > 0 {
		port = stored.PreviewPort
	}
	if err := m.Store.SetPreview(ctx, runID, "failed", "", port, nil); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) lockPublication(runID string) func() {
	m.publishMu.Lock()
	lock := m.publishLocks[runID]
	if lock == nil {
		lock = &publicationLock{}
		m.publishLocks[runID] = lock
	}
	lock.users++
	m.publishMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		m.publishMu.Lock()
		lock.users--
		if lock.users == 0 && m.publishLocks[runID] == lock {
			delete(m.publishLocks, runID)
		}
		m.publishMu.Unlock()
	}
}

// Expire removes the broker target, stops the forward, and marks the run's
// preview as expired. It does not destroy the sandbox; the scheduler owns
// sandbox teardown.
func (m *Manager) Expire(ctx context.Context, run model.Run) {
	m.Broker.Remove(run.ID)
	if err := m.Store.SetPreview(ctx, run.ID, "expired", "", run.PreviewPort, nil); err != nil && m.Logger != nil {
		m.Logger.Error("mark preview expired", "run", run.ID, "error", err)
	}
}

// Restore re-establishes forwards and capabilities for previews that were
// published before a control-plane restart and have not expired yet.
func (m *Manager) Restore(ctx context.Context) {
	if !m.Enabled() {
		return
	}
	runs, err := m.Store.ListRuns(ctx, model.RunFilter{State: "completed", Limit: 500})
	if err != nil {
		if m.Logger != nil {
			m.Logger.Error("list runs for preview restore", "error", err)
		}
		return
	}
	now := time.Now()
	for _, run := range runs {
		if run.PreviewState != "published" || run.SandboxID == "" || run.PreviewPort <= 0 {
			continue
		}
		if run.PreviewExpiresAt == nil || now.After(*run.PreviewExpiresAt) {
			m.Expire(ctx, run)
			continue
		}
		token := capabilityFromURL(run.PreviewURL)
		if token == "" {
			m.Expire(ctx, run)
			continue
		}
		localURL, stop, err := m.Forwarder.Forward(ctx, run.SandboxID, run.PreviewPort)
		if err != nil {
			if m.Logger != nil {
				m.Logger.Error("restore preview forward", "run", run.ID, "error", err)
			}
			m.Expire(ctx, run)
			continue
		}
		if err := m.Broker.Register(run.ID, localURL, stop); err != nil {
			stop()
			m.Expire(ctx, run)
			continue
		}
		deadline := run.CreatedAt.Add(m.MaxAge)
		if run.CompletedAt != nil {
			deadline = run.CompletedAt.Add(m.MaxAge)
		}
		_ = m.Broker.RestoreCapability(token, run.ID, *run.PreviewExpiresAt, deadline)
	}
}

func (m *Manager) publicPreviewURL(runID, token string) (string, error) {
	parsed, err := url.Parse(m.PublicURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "localhost" || net.ParseIP(parsed.Hostname()).IsLoopback() {
		return "", fmt.Errorf("preview public origin must be a non-loopback HTTPS URL")
	}
	return m.PublicURL + "/previews/" + url.PathEscape(runID) + "/?cap=" + url.QueryEscape(token), nil
}

func capabilityFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("cap")
}

func (m *Manager) recordActivity(runID string, expiresAt time.Time) {
	m.mu.Lock()
	last, ok := m.lastPersist[runID]
	if ok && time.Since(last) < 30*time.Second {
		m.mu.Unlock()
		return
	}
	m.lastPersist[runID] = time.Now()
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run, err := m.Store.GetRun(ctx, runID)
	if err != nil {
		return
	}
	value := expiresAt.UTC()
	if err := m.Store.SetPreview(ctx, runID, run.PreviewState, run.PreviewURL, run.PreviewPort, &value); err != nil && m.Logger != nil {
		m.Logger.Error("persist preview activity", "run", runID, "error", err)
	}
}
