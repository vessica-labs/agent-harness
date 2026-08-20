package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/preview"
)

// SetPreviewManager wires the preview lifecycle into the control plane and
// installs the overlay and panel served on top of proxied preview pages.
func (s *Server) SetPreviewManager(manager *preview.Manager) {
	s.previewMu.Lock()
	s.preview = manager
	s.previewMu.Unlock()
	if manager == nil {
		return
	}
	manager.Broker.SetOverlayProvider(preview.Overlay)
	manager.Broker.SetPanelHandler(preview.PanelHandler(func(runID string) (preview.PanelData, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		run, err := s.store.GetRun(ctx, runID)
		if err != nil {
			return preview.PanelData{}, false
		}
		return preview.PanelData{RunID: run.ID, IssueKey: run.SourceIssueKey,
			PullRequestURL: run.PullRequestURL, ExpiresAt: run.PreviewExpiresAt}, true
	}))
}

func (s *Server) previewManager() *preview.Manager {
	s.previewMu.RLock()
	defer s.previewMu.RUnlock()
	return s.preview
}

// previewRoutes serves /previews/ requests. When an edge token is configured
// only the preview-edge service may reach these routes; the shared header is
// overwritten at the edge so callers cannot supply it.
func (s *Server) previewRoutes(w http.ResponseWriter, r *http.Request) {
	manager := s.previewManager()
	if manager == nil {
		http.NotFound(w, r)
		return
	}
	if token := s.config.PreviewEdgeToken; token != "" {
		presented := r.Header.Get(preview.EdgeHeader)
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			writeError(w, http.StatusForbidden, errors.New("previews are only served through the preview edge"))
			return
		}
	}
	manager.Broker.ServeHTTP(w, r)
}

// publishPreview runs after run.completed when the worker reported a healthy
// preview port. It forwards the sandbox port, mints the capability link,
// records the published event, and posts the link to the Linear parent issue.
func (s *Server) publishPreview(runID string) {
	manager := s.previewManager()
	if manager == nil || !manager.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || run.PreviewState != "ready" || run.PreviewPort <= 0 || run.SandboxID == "" {
		return
	}
	url, err := manager.Publish(ctx, run)
	if err != nil {
		s.logger.Error("publish run preview", "run_id", runID, "error", err)
		s.appendEvent(ctx, model.Event{RunID: runID, SourceIssueID: run.SourceIssueID, Type: "preview.failed",
			Level: "warning", Message: "Preview could not be published: " + err.Error()})
		return
	}
	s.appendEvent(ctx, model.Event{RunID: runID, SourceIssueID: run.SourceIssueID, Type: "preview.published",
		Level: "info", Message: "Preview is available"})
	if err := s.syncLinearPreview(ctx, run, url); err != nil {
		s.logger.Error("post preview link to Linear", "run_id", runID, "error", err)
	}
	s.broker.Notify()
}

// syncLinearPreview upserts a marker-keyed preview comment on the run's
// parent Linear issue so republishing updates the same comment.
func (s *Server) syncLinearPreview(ctx context.Context, run model.Run, url string) error {
	token, err := s.linearAccessToken(ctx)
	if err != nil {
		return nil
	}
	client := s.linear(token)
	marker := "<!-- agent-harness:preview:" + run.ID + " -->"
	body := marker + "\n\n## Preview\n\n[Open live preview](" + url + ")\n\n" +
		"The preview stays available while it is being used and shuts down automatically afterward."
	if run.PullRequestURL != "" {
		body += "\n\nDraft pull request: " + run.PullRequestURL
	}
	comment, err := client.UpsertComment(ctx, run.SourceIssueID, marker, body)
	if err != nil {
		return fmt.Errorf("upsert Linear preview comment: %w", err)
	}
	return s.store.PutExternalSync(ctx, model.ExternalSync{RunID: run.ID, LogicalKey: "preview-link",
		Provider: "linear", State: "synced", Marker: marker, ExternalID: comment.ID})
}
