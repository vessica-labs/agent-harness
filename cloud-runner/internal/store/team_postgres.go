package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

func (p *Postgres) GetInstallationState(ctx context.Context) (model.InstallationState, error) {
	var value model.InstallationState
	err := p.pool.QueryRow(ctx, `SELECT owner_member_id,initialized_at FROM installation_state WHERE singleton=true`).Scan(&value.OwnerMemberID, &value.InitializedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, nil
	}
	value.Initialized = err == nil
	return value, err
}

func (p *Postgres) InitializeTeam(ctx context.Context, member model.Member, session model.MemberSession, audit model.AuthAudit) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM installation_state)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO members(id,display_name,role,state) VALUES($1,$2,$3,$4)`, member.ID, member.DisplayName, member.Role, member.State); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO member_sessions(id,member_id,device_name,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, session.ID, session.MemberID, session.DeviceName, session.AccessTokenHash, session.RefreshTokenHash, session.AccessExpiresAt, session.RefreshExpiresAt); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO installation_state(singleton,owner_member_id) VALUES(true,$1)`, member.ID); err != nil {
		return err
	}
	if err = insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) CreateInvitation(ctx context.Context, v model.Invitation, audit model.AuthAudit) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO invitations(id,role,created_by,label,secret_hash,max_uses,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, v.ID, v.Role, v.CreatedBy, v.Label, v.SecretHash, v.MaxUses, v.ExpiresAt)
	if err != nil {
		return err
	}
	if err = insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanInvitation(row rowScanner) (model.Invitation, error) {
	var v model.Invitation
	err := row.Scan(&v.ID, &v.Role, &v.CreatedBy, &v.Label, &v.SecretHash, &v.MaxUses, &v.UseCount, &v.ExpiresAt, &v.CreatedAt, &v.RevokedAt, &v.ConsumedAt)
	return v, err
}
func (p *Postgres) ListInvitations(ctx context.Context, activeOnly bool) ([]model.Invitation, error) {
	query := `SELECT id,role,created_by,label,secret_hash,max_uses,use_count,expires_at,created_at,revoked_at,consumed_at FROM invitations`
	if activeOnly {
		query += ` WHERE revoked_at IS NULL AND expires_at>now() AND use_count<max_uses`
	}
	query += ` ORDER BY created_at DESC`
	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Invitation{}
	for rows.Next() {
		v, e := scanInvitation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (p *Postgres) RevokeInvitation(ctx context.Context, id, actor string, audit model.AuthAudit) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE invitations SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	audit.ActorID = actor
	if err = insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) RedeemInvitation(ctx context.Context, hash []byte, member model.Member, session model.MemberSession, audit model.AuthAudit) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var invite model.Invitation
	err = tx.QueryRow(ctx, `SELECT id,role,created_by,label,secret_hash,max_uses,use_count,expires_at,created_at,revoked_at,consumed_at FROM invitations WHERE secret_hash=$1 FOR UPDATE`, hash).Scan(&invite.ID, &invite.Role, &invite.CreatedBy, &invite.Label, &invite.SecretHash, &invite.MaxUses, &invite.UseCount, &invite.ExpiresAt, &invite.CreatedAt, &invite.RevokedAt, &invite.ConsumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if invite.RevokedAt != nil || !invite.ExpiresAt.After(now) || invite.UseCount >= invite.MaxUses {
		return ErrConflict
	}
	member.Role = invite.Role
	if _, err = tx.Exec(ctx, `INSERT INTO members(id,display_name,role,state) VALUES($1,$2,$3,$4)`, member.ID, member.DisplayName, member.Role, member.State); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO member_sessions(id,member_id,device_name,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, session.ID, session.MemberID, session.DeviceName, session.AccessTokenHash, session.RefreshTokenHash, session.AccessExpiresAt, session.RefreshExpiresAt); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE invitations SET use_count=use_count+1,consumed_at=CASE WHEN use_count+1>=max_uses THEN now() ELSE consumed_at END WHERE id=$1`, invite.ID); err != nil {
		return err
	}
	if err = insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const memberColumns = `id,display_name,role,state,created_at,updated_at,last_seen_at`

func scanMember(row rowScanner) (model.Member, error) {
	var v model.Member
	err := row.Scan(&v.ID, &v.DisplayName, &v.Role, &v.State, &v.CreatedAt, &v.UpdatedAt, &v.LastSeenAt)
	return v, err
}

const sessionColumns = `id,member_id,device_name,access_token_hash,refresh_token_hash,previous_refresh_token_hash,access_expires_at,refresh_expires_at,created_at,updated_at,last_seen_at,revoked_at,revoked_reason`

func scanMemberSession(row rowScanner) (model.MemberSession, error) {
	var v model.MemberSession
	err := row.Scan(&v.ID, &v.MemberID, &v.DeviceName, &v.AccessTokenHash, &v.RefreshTokenHash, &v.PreviousRefreshTokenHash, &v.AccessExpiresAt, &v.RefreshExpiresAt, &v.CreatedAt, &v.UpdatedAt, &v.LastSeenAt, &v.RevokedAt, &v.RevokedReason)
	return v, err
}

func (p *Postgres) AuthenticateSession(ctx context.Context, hash []byte, now time.Time) (model.Principal, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return model.Principal{}, err
	}
	defer tx.Rollback(ctx)
	var out model.Principal
	err = tx.QueryRow(ctx, `SELECT m.id,m.display_name,m.role,m.state,m.created_at,m.updated_at,m.last_seen_at,
s.id,s.member_id,s.device_name,s.access_token_hash,s.refresh_token_hash,s.previous_refresh_token_hash,
s.access_expires_at,s.refresh_expires_at,s.created_at,s.updated_at,s.last_seen_at,s.revoked_at,s.revoked_reason
FROM member_sessions s JOIN members m ON m.id=s.member_id
WHERE s.access_token_hash=$1 AND s.revoked_at IS NULL AND s.access_expires_at>$2 AND m.state='active'`, hash, now).Scan(&out.Member.ID, &out.Member.DisplayName, &out.Member.Role, &out.Member.State, &out.Member.CreatedAt, &out.Member.UpdatedAt, &out.Member.LastSeenAt, &out.Session.ID, &out.Session.MemberID, &out.Session.DeviceName, &out.Session.AccessTokenHash, &out.Session.RefreshTokenHash, &out.Session.PreviousRefreshTokenHash, &out.Session.AccessExpiresAt, &out.Session.RefreshExpiresAt, &out.Session.CreatedAt, &out.Session.UpdatedAt, &out.Session.LastSeenAt, &out.Session.RevokedAt, &out.Session.RevokedReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE member_sessions SET last_seen_at=$2,updated_at=$2 WHERE id=$1`, out.Session.ID, now)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE members SET last_seen_at=$2,updated_at=$2 WHERE id=$1`, out.Member.ID, now)
	}
	if err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	out.Session.LastSeenAt = &now
	out.Member.LastSeenAt = &now
	return out, nil
}

func (p *Postgres) RefreshSession(ctx context.Context, oldHash, newAccess, newRefresh []byte, accessExpiry, refreshExpiry time.Time) (model.SessionRefreshResult, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.SessionRefreshResult{}, err
	}
	defer tx.Rollback(ctx)
	now := time.Now().UTC()
	var reusedID string
	err = tx.QueryRow(ctx, `SELECT id FROM member_sessions WHERE previous_refresh_token_hash=$1 AND revoked_at IS NULL FOR UPDATE`, oldHash).Scan(&reusedID)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE member_sessions SET revoked_at=$2,revoked_reason='refresh_token_reuse',updated_at=$2 WHERE id=$1`, reusedID, now)
		if err != nil {
			return model.SessionRefreshResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return model.SessionRefreshResult{}, err
		}
		return model.SessionRefreshResult{Reused: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.SessionRefreshResult{}, err
	}
	var s model.MemberSession
	err = tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM member_sessions WHERE refresh_token_hash=$1 AND revoked_at IS NULL AND refresh_expires_at>$2 FOR UPDATE`, oldHash, now).Scan(&s.ID, &s.MemberID, &s.DeviceName, &s.AccessTokenHash, &s.RefreshTokenHash, &s.PreviousRefreshTokenHash, &s.AccessExpiresAt, &s.RefreshExpiresAt, &s.CreatedAt, &s.UpdatedAt, &s.LastSeenAt, &s.RevokedAt, &s.RevokedReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.SessionRefreshResult{}, ErrNotFound
	}
	if err != nil {
		return model.SessionRefreshResult{}, err
	}
	m, err := scanMember(tx.QueryRow(ctx, `SELECT `+memberColumns+` FROM members WHERE id=$1 AND state='active'`, s.MemberID))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.SessionRefreshResult{}, ErrNotFound
	}
	if err != nil {
		return model.SessionRefreshResult{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE member_sessions SET previous_refresh_token_hash=refresh_token_hash,access_token_hash=$2,refresh_token_hash=$3,access_expires_at=$4,refresh_expires_at=$5,updated_at=$6 WHERE id=$1`, s.ID, newAccess, newRefresh, accessExpiry, refreshExpiry, now)
	if err != nil {
		return model.SessionRefreshResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.SessionRefreshResult{}, err
	}
	s.PreviousRefreshTokenHash = s.RefreshTokenHash
	s.AccessTokenHash = newAccess
	s.RefreshTokenHash = newRefresh
	s.AccessExpiresAt = accessExpiry
	s.RefreshExpiresAt = refreshExpiry
	s.UpdatedAt = now
	return model.SessionRefreshResult{Principal: model.Principal{Member: m, Session: s}}, nil
}

func (p *Postgres) ListMembers(ctx context.Context) ([]model.Member, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+memberColumns+` FROM members ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Member{}
	for rows.Next() {
		v, e := scanMember(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (p *Postgres) UpdateMemberRole(ctx context.Context, id, role, actor string, audit model.AuthAudit) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE members SET role=$2,updated_at=now() WHERE id=$1 AND role<>'owner'`, id, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	audit.ActorID = actor
	if err = insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (p *Postgres) RevokeMember(ctx context.Context, id, reason, actor string, audit model.AuthAudit) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE members SET state='revoked',updated_at=now() WHERE id=$1 AND role<>'owner'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE member_sessions SET revoked_at=COALESCE(revoked_at,now()),revoked_reason=$2,updated_at=now() WHERE member_id=$1`, id, reason)
	if err != nil {
		return err
	}
	audit.ActorID = actor
	if err = insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (p *Postgres) ListMemberSessions(ctx context.Context, memberID string) ([]model.MemberSession, error) {
	query := `SELECT ` + sessionColumns + ` FROM member_sessions`
	args := []any{}
	if memberID != "" {
		query += ` WHERE member_id=$1`
		args = append(args, memberID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MemberSession{}
	for rows.Next() {
		v, e := scanMemberSession(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (p *Postgres) RevokeMemberSession(ctx context.Context, id, reason, actor string, audit model.AuthAudit) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE member_sessions SET revoked_at=COALESCE(revoked_at,now()),revoked_reason=$2,updated_at=now() WHERE id=$1`, id, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	audit.ActorID = actor
	if err = insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func insertAudit(ctx context.Context, tx pgx.Tx, v model.AuthAudit) error {
	details := v.Details
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	_, err := tx.Exec(ctx, `INSERT INTO auth_audit_log(member_id,session_id,actor_id,action,target_id,details) VALUES(NULLIF($1,''),NULLIF($2,''),NULLIF($3,''),$4,$5,$6)`, v.MemberID, v.SessionID, v.ActorID, v.Action, v.TargetID, details)
	return err
}
func (p *Postgres) AppendAuthAudit(ctx context.Context, v model.AuthAudit) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = insertAudit(ctx, tx, v); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (p *Postgres) ListAuthAudit(ctx context.Context, limit int) ([]model.AuthAudit, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT id,COALESCE(member_id,''),COALESCE(session_id,''),COALESCE(actor_id,''),action,target_id,details,created_at FROM auth_audit_log ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuthAudit{}
	for rows.Next() {
		var v model.AuthAudit
		if err := rows.Scan(&v.ID, &v.MemberID, &v.SessionID, &v.ActorID, &v.Action, &v.TargetID, &v.Details, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
