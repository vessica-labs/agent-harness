package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

const (
	accessTokenLifetime  = 15 * time.Minute
	refreshTokenLifetime = 30 * 24 * time.Hour
)

type principalContextKey struct{}
type accessDigestContextKey struct{}

type tokenPair struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

func newID(prefix string) (string, error) {
	value, err := secure.RandomToken()
	if err != nil {
		return "", err
	}
	return prefix + "_" + value[:18], nil
}

func (s *Server) mintSession(memberID, deviceName string, now time.Time) (model.MemberSession, tokenPair, error) {
	access, err := secure.RandomToken()
	if err != nil {
		return model.MemberSession{}, tokenPair{}, err
	}
	refresh, err := secure.RandomToken()
	if err != nil {
		return model.MemberSession{}, tokenPair{}, err
	}
	id, err := newID("session")
	if err != nil {
		return model.MemberSession{}, tokenPair{}, err
	}
	pair := tokenPair{AccessToken: access, AccessExpiresAt: now.Add(accessTokenLifetime), RefreshToken: refresh, RefreshExpiresAt: now.Add(refreshTokenLifetime)}
	session := model.MemberSession{ID: id, MemberID: memberID, DeviceName: deviceName, AccessTokenHash: s.box.TokenDigest("access", access), RefreshTokenHash: s.box.TokenDigest("refresh", refresh), AccessExpiresAt: pair.AccessExpiresAt, RefreshExpiresAt: pair.RefreshExpiresAt, CreatedAt: now}
	session.UpdatedAt = now
	return session, pair, nil
}

func (s *Server) initializeTeam(w http.ResponseWriter, r *http.Request) {
	if !secure.EqualSecret(secure.Bearer(r.Header.Get("Authorization")), s.config.ManagementToken) {
		writeError(w, http.StatusUnauthorized, errors.New("bootstrap bearer token required"))
		return
	}
	state, err := s.store.GetInstallationState(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if state.Initialized {
		writeError(w, http.StatusConflict, errors.New("team access is already initialized"))
		return
	}
	var input struct {
		DisplayName string `json:"display_name"`
		DeviceName  string `json:"device_name"`
	}
	if err := decodeJSON(w, r, s.config.MaxRequestBytes, &input); err != nil {
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.DeviceName = strings.TrimSpace(input.DeviceName)
	if input.DisplayName == "" || input.DeviceName == "" {
		writeError(w, http.StatusBadRequest, errors.New("display_name and device_name are required"))
		return
	}
	now := time.Now().UTC()
	memberID, err := newID("member")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	member := model.Member{ID: memberID, DisplayName: input.DisplayName, Role: "owner", State: "active", CreatedAt: now, UpdatedAt: now}
	session, pair, err := s.mintSession(member.ID, input.DeviceName, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	audit := model.AuthAudit{MemberID: member.ID, SessionID: session.ID, ActorID: member.ID, Action: "team.initialized", TargetID: member.ID}
	if err = s.store.InitializeTeam(r.Context(), member, session, audit); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"member": member, "session": session, "tokens": pair})
}

func (s *Server) redeemInvitation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		InviteToken string `json:"invite_token"`
		DisplayName string `json:"display_name"`
		DeviceName  string `json:"device_name"`
	}
	if err := decodeJSON(w, r, s.config.MaxRequestBytes, &input); err != nil {
		return
	}
	input.InviteToken = strings.TrimSpace(input.InviteToken)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.DeviceName = strings.TrimSpace(input.DeviceName)
	if input.InviteToken == "" || input.DisplayName == "" || input.DeviceName == "" {
		writeError(w, http.StatusBadRequest, errors.New("invite_token, display_name, and device_name are required"))
		return
	}
	now := time.Now().UTC()
	memberID, err := newID("member")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	member := model.Member{ID: memberID, DisplayName: input.DisplayName, State: "active", CreatedAt: now, UpdatedAt: now}
	session, pair, err := s.mintSession(member.ID, input.DeviceName, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	audit := model.AuthAudit{MemberID: member.ID, SessionID: session.ID, ActorID: member.ID, Action: "invitation.redeemed", TargetID: member.ID}
	err = s.store.RedeemInvitation(r.Context(), s.box.TokenDigest("invite", input.InviteToken), member, session, audit)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusGone, errors.New("invitation is invalid, expired, revoked, or already used"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	principal, err := s.store.AuthenticateSession(r.Context(), session.AccessTokenHash, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"member": principal.Member, "session": principal.Session, "tokens": pair})
}

func (s *Server) refreshToken(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(w, r, s.config.MaxRequestBytes, &input); err != nil {
		return
	}
	if strings.TrimSpace(input.RefreshToken) == "" {
		writeError(w, http.StatusBadRequest, errors.New("refresh_token is required"))
		return
	}
	access, err := secure.RandomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	refresh, err := secure.RandomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	pair := tokenPair{AccessToken: access, AccessExpiresAt: now.Add(accessTokenLifetime), RefreshToken: refresh, RefreshExpiresAt: now.Add(refreshTokenLifetime)}
	result, err := s.store.RefreshSession(r.Context(), s.box.TokenDigest("refresh", input.RefreshToken), s.box.TokenDigest("access", access), s.box.TokenDigest("refresh", refresh), pair.AccessExpiresAt, pair.RefreshExpiresAt)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errors.New("refresh token is invalid or expired"))
		return
	}
	if result.Reused {
		_ = s.store.AppendAuthAudit(r.Context(), model.AuthAudit{Action: "session.refresh_reuse", Details: json.RawMessage(`{"result":"session_revoked"}`)})
		writeError(w, http.StatusUnauthorized, errors.New("refresh token reuse detected; device session revoked"))
		return
	}
	_ = s.store.AppendAuthAudit(r.Context(), model.AuthAudit{MemberID: result.Principal.Member.ID, SessionID: result.Principal.Session.ID, ActorID: result.Principal.Member.ID, Action: "session.refreshed", TargetID: result.Principal.Session.ID})
	writeJSON(w, http.StatusOK, map[string]any{"member": result.Principal.Member, "session": result.Principal.Session, "tokens": pair})
}

func (s *Server) joinPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	const page = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Join Agent Harness</title><style>body{font:16px system-ui;background:#0b1020;color:#e7edf9;max-width:720px;margin:10vh auto;padding:24px}main{border:1px solid #29334b;border-radius:16px;padding:28px;background:#11182a}code{display:block;overflow:auto;padding:14px;background:#080c16;border-radius:9px;color:#9dd6ff}small{color:#9ca9c3}</style></head><body><main><h1>Join Agent Harness</h1><p>This one-time invitation gives this device its own revocable team session.</p><code id="command">Loading invitation…</code><p><small>Paste the command into a terminal, or paste this complete link into Codex and ask it to join the control plane. The invitation expires after use.</small></p></main><script>const t=new URLSearchParams(location.hash.slice(1)).get('invite');document.getElementById('command').textContent=t?'agent-harness cloud join '+JSON.stringify(location.href):'This invitation link is missing its secret.';</script></body></html>`
	_, _ = w.Write([]byte(page))
}

func principalFrom(ctx context.Context) model.Principal {
	value, _ := ctx.Value(principalContextKey{}).(model.Principal)
	return value
}
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, principalFrom(r.Context()))
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	audit := model.AuthAudit{MemberID: p.Member.ID, SessionID: p.Session.ID, ActorID: p.Member.ID, Action: "session.logged_out", TargetID: p.Session.ID}
	if err := s.store.RevokeMemberSession(r.Context(), p.Session.ID, "logout", p.Member.ID, audit); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) teamRoutes(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	part := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/team/"), "/")
	segments := strings.Split(part, "/")
	switch {
	case part == "members" && r.Method == http.MethodGet:
		values, err := s.store.ListMembers(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"members": values})
	case len(segments) == 2 && segments[0] == "members" && r.Method == http.MethodPatch:
		var input struct {
			Role string `json:"role"`
		}
		if err := decodeJSON(w, r, s.config.MaxRequestBytes, &input); err != nil {
			return
		}
		if !validRole(input.Role) || input.Role == "owner" {
			writeError(w, http.StatusBadRequest, errors.New("role must be viewer, operator, or admin"))
			return
		}
		audit := model.AuthAudit{ActorID: p.Member.ID, Action: "member.role_updated", TargetID: segments[1]}
		if err := s.store.UpdateMemberRole(r.Context(), segments[1], input.Role, p.Member.ID, audit); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case len(segments) == 2 && segments[0] == "members" && r.Method == http.MethodDelete:
		audit := model.AuthAudit{ActorID: p.Member.ID, Action: "member.revoked", TargetID: segments[1]}
		if err := s.store.RevokeMember(r.Context(), segments[1], "revoked_by_admin", p.Member.ID, audit); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case part == "invitations" && r.Method == http.MethodGet:
		values, err := s.store.ListInvitations(r.Context(), false)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"invitations": values})
	case part == "invitations" && r.Method == http.MethodPost:
		s.createInvitation(w, r, p)
	case len(segments) == 2 && segments[0] == "invitations" && r.Method == http.MethodDelete:
		audit := model.AuthAudit{ActorID: p.Member.ID, Action: "invitation.revoked", TargetID: segments[1]}
		if err := s.store.RevokeInvitation(r.Context(), segments[1], p.Member.ID, audit); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case part == "sessions" && r.Method == http.MethodGet:
		values, err := s.store.ListMemberSessions(r.Context(), r.URL.Query().Get("member_id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": values})
	case len(segments) == 2 && segments[0] == "sessions" && r.Method == http.MethodDelete:
		audit := model.AuthAudit{ActorID: p.Member.ID, Action: "session.revoked", TargetID: segments[1]}
		if err := s.store.RevokeMemberSession(r.Context(), segments[1], "revoked_by_admin", p.Member.ID, audit); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case part == "audit" && r.Method == http.MethodGet:
		values, err := s.store.ListAuthAudit(r.Context(), queryInt(r, "limit", 100))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": values})
	default:
		writeError(w, http.StatusNotFound, store.ErrNotFound)
	}
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request, p model.Principal) {
	var input struct {
		Role             string `json:"role"`
		Label            string `json:"label"`
		ExpiresInMinutes int    `json:"expires_in_minutes"`
	}
	if err := decodeJSON(w, r, s.config.MaxRequestBytes, &input); err != nil {
		return
	}
	if !validRole(input.Role) || input.Role == "owner" {
		writeError(w, http.StatusBadRequest, errors.New("role must be viewer, operator, or admin"))
		return
	}
	if input.ExpiresInMinutes == 0 {
		input.ExpiresInMinutes = 60
	}
	if input.ExpiresInMinutes < 1 || input.ExpiresInMinutes > 7*24*60 {
		writeError(w, http.StatusBadRequest, errors.New("invite expiry must be between 1 minute and 7 days"))
		return
	}
	secret, err := secure.RandomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	id, err := newID("invite")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	invite := model.Invitation{ID: id, Role: input.Role, CreatedBy: p.Member.ID, Label: strings.TrimSpace(input.Label), SecretHash: s.box.TokenDigest("invite", secret), MaxUses: 1, ExpiresAt: now.Add(time.Duration(input.ExpiresInMinutes) * time.Minute), CreatedAt: now}
	audit := model.AuthAudit{ActorID: p.Member.ID, Action: "invitation.created", TargetID: id, Details: json.RawMessage(fmt.Sprintf(`{"role":%q}`, input.Role))}
	if err := s.store.CreateInvitation(r.Context(), invite, audit); err != nil {
		writeStoreError(w, err)
		return
	}
	base := strings.TrimRight(s.config.PublicURL, "/")
	if base == "" {
		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		base = scheme + "://" + r.Host
	}
	joinURL := base + "/join#invite=" + url.QueryEscape(secret)
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": invite, "join_url": joinURL})
}

func validRole(role string) bool {
	switch role {
	case "viewer", "operator", "admin", "owner":
		return true
	}
	return false
}
func roleRank(role string) int {
	switch role {
	case "viewer":
		return 1
	case "operator":
		return 2
	case "admin":
		return 3
	case "owner":
		return 4
	}
	return 0
}
func roleAllows(actual, required string) bool { return roleRank(actual) >= roleRank(required) }
func requiredRole(r *http.Request) string {
	p := r.URL.Path
	if p == "/v1/status" || p == "/v1/whoami" || p == "/v1/logout" || p == "/v1/runs" || p == "/v1/events" || p == "/v1/input-requests" {
		if r.Method == http.MethodGet || p == "/v1/logout" {
			return "viewer"
		}
	}
	if strings.HasPrefix(p, "/v1/runs/") {
		if r.Method == http.MethodGet {
			return "viewer"
		}
		return "operator"
	}
	if strings.HasPrefix(p, "/v1/input-requests/") {
		if r.Method == http.MethodGet {
			return "viewer"
		}
		return "operator"
	}
	if strings.Contains(p, "/linear/issues") {
		return "operator"
	}
	return "admin"
}
