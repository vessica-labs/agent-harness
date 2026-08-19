package store

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

func (m *Memory) ensureTeamMaps() {
	if m.members == nil {
		m.members = map[string]model.Member{}
	}
	if m.invitations == nil {
		m.invitations = map[string]model.Invitation{}
	}
	if m.memberSessions == nil {
		m.memberSessions = map[string]model.MemberSession{}
	}
}

func (m *Memory) GetInstallationState(context.Context) (model.InstallationState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installation, nil
}

func (m *Memory) InitializeTeam(_ context.Context, member model.Member, session model.MemberSession, audit model.AuthAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	if m.installation.Initialized {
		return ErrConflict
	}
	now := time.Now().UTC()
	if member.CreatedAt.IsZero() {
		member.CreatedAt = now
	}
	member.UpdatedAt = now
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	session.UpdatedAt = now
	m.members[member.ID], m.memberSessions[session.ID] = member, session
	m.installation = model.InstallationState{Initialized: true, OwnerMemberID: member.ID, InitializedAt: &now}
	m.appendAuditLocked(audit)
	return nil
}

func (m *Memory) CreateInvitation(_ context.Context, invite model.Invitation, audit model.AuthAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	for _, v := range m.invitations {
		if bytes.Equal(v.SecretHash, invite.SecretHash) {
			return ErrConflict
		}
	}
	if invite.CreatedAt.IsZero() {
		invite.CreatedAt = time.Now().UTC()
	}
	m.invitations[invite.ID] = invite
	m.appendAuditLocked(audit)
	return nil
}

func (m *Memory) ListInvitations(_ context.Context, activeOnly bool) ([]model.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	now := time.Now().UTC()
	result := []model.Invitation{}
	for _, v := range m.invitations {
		if activeOnly && (v.RevokedAt != nil || !v.ExpiresAt.After(now) || v.UseCount >= v.MaxUses) {
			continue
		}
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (m *Memory) RevokeInvitation(_ context.Context, id, actor string, audit model.AuthAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	v, ok := m.invitations[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	v.RevokedAt = &now
	m.invitations[id] = v
	audit.ActorID = actor
	m.appendAuditLocked(audit)
	return nil
}

func (m *Memory) RedeemInvitation(_ context.Context, hash []byte, member model.Member, session model.MemberSession, audit model.AuthAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	now := time.Now().UTC()
	var id string
	var invite model.Invitation
	for key, v := range m.invitations {
		if bytes.Equal(v.SecretHash, hash) {
			id, invite = key, v
			break
		}
	}
	if id == "" {
		return ErrNotFound
	}
	if invite.RevokedAt != nil || !invite.ExpiresAt.After(now) || invite.UseCount >= invite.MaxUses {
		return ErrConflict
	}
	member.Role = invite.Role
	if member.CreatedAt.IsZero() {
		member.CreatedAt = now
	}
	member.UpdatedAt = now
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	session.UpdatedAt = now
	invite.UseCount++
	if invite.UseCount >= invite.MaxUses {
		invite.ConsumedAt = &now
	}
	m.invitations[id] = invite
	m.members[member.ID] = member
	m.memberSessions[session.ID] = session
	m.appendAuditLocked(audit)
	return nil
}

func (m *Memory) AuthenticateSession(_ context.Context, hash []byte, now time.Time) (model.Principal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	for id, s := range m.memberSessions {
		if !bytes.Equal(s.AccessTokenHash, hash) {
			continue
		}
		member, ok := m.members[s.MemberID]
		if !ok || member.State != "active" || s.RevokedAt != nil || !s.AccessExpiresAt.After(now) {
			return model.Principal{}, ErrNotFound
		}
		t := now.UTC()
		s.LastSeenAt = &t
		s.UpdatedAt = t
		member.LastSeenAt = &t
		member.UpdatedAt = t
		m.memberSessions[id] = s
		m.members[member.ID] = member
		return model.Principal{Member: member, Session: s}, nil
	}
	return model.Principal{}, ErrNotFound
}

func (m *Memory) RefreshSession(_ context.Context, oldHash, newAccess, newRefresh []byte, accessExpiry, refreshExpiry time.Time) (model.SessionRefreshResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	now := time.Now().UTC()
	for id, s := range m.memberSessions {
		if bytes.Equal(s.PreviousRefreshTokenHash, oldHash) {
			s.RevokedAt = &now
			s.RevokedReason = "refresh_token_reuse"
			m.memberSessions[id] = s
			return model.SessionRefreshResult{Reused: true}, nil
		}
	}
	for id, s := range m.memberSessions {
		if !bytes.Equal(s.RefreshTokenHash, oldHash) {
			continue
		}
		member, ok := m.members[s.MemberID]
		if !ok || member.State != "active" || s.RevokedAt != nil || !s.RefreshExpiresAt.After(now) {
			return model.SessionRefreshResult{}, ErrNotFound
		}
		s.PreviousRefreshTokenHash = append([]byte(nil), s.RefreshTokenHash...)
		s.AccessTokenHash = append([]byte(nil), newAccess...)
		s.RefreshTokenHash = append([]byte(nil), newRefresh...)
		s.AccessExpiresAt = accessExpiry
		s.RefreshExpiresAt = refreshExpiry
		s.UpdatedAt = now
		m.memberSessions[id] = s
		return model.SessionRefreshResult{Principal: model.Principal{Member: member, Session: s}}, nil
	}
	return model.SessionRefreshResult{}, ErrNotFound
}

func (m *Memory) ListMembers(context.Context) ([]model.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	out := []model.Member{}
	for _, v := range m.members {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) UpdateMemberRole(_ context.Context, id, role, actor string, audit model.AuthAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	v, ok := m.members[id]
	if !ok {
		return ErrNotFound
	}
	if v.Role == "owner" {
		return ErrConflict
	}
	v.Role = role
	v.UpdatedAt = time.Now().UTC()
	m.members[id] = v
	audit.ActorID = actor
	m.appendAuditLocked(audit)
	return nil
}

func (m *Memory) RevokeMember(_ context.Context, id, reason, actor string, audit model.AuthAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	v, ok := m.members[id]
	if !ok {
		return ErrNotFound
	}
	if v.Role == "owner" {
		return ErrConflict
	}
	now := time.Now().UTC()
	v.State = "revoked"
	v.UpdatedAt = now
	m.members[id] = v
	for sid, s := range m.memberSessions {
		if s.MemberID == id && s.RevokedAt == nil {
			s.RevokedAt = &now
			s.RevokedReason = reason
			m.memberSessions[sid] = s
		}
	}
	audit.ActorID = actor
	m.appendAuditLocked(audit)
	return nil
}

func (m *Memory) ListMemberSessions(_ context.Context, memberID string) ([]model.MemberSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	out := []model.MemberSession{}
	for _, v := range m.memberSessions {
		if memberID == "" || v.MemberID == memberID {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) RevokeMemberSession(_ context.Context, id, reason, actor string, audit model.AuthAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTeamMaps()
	v, ok := m.memberSessions[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	v.RevokedAt = &now
	v.RevokedReason = reason
	v.UpdatedAt = now
	m.memberSessions[id] = v
	audit.ActorID = actor
	m.appendAuditLocked(audit)
	return nil
}

func (m *Memory) AppendAuthAudit(_ context.Context, v model.AuthAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendAuditLocked(v)
	return nil
}
func (m *Memory) appendAuditLocked(v model.AuthAudit) {
	m.auditSeq++
	v.ID = m.auditSeq
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	m.authAudit = append(m.authAudit, v)
}
func (m *Memory) ListAuthAudit(_ context.Context, limit int) ([]model.AuthAudit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	start := len(m.authAudit) - limit
	if start < 0 {
		start = 0
	}
	out := append([]model.AuthAudit(nil), m.authAudit[start:]...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
