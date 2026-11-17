// Package postgres implements store.Store on PostgreSQL with pgx.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexostream/control-plane/store"
	"nexostream/internal/model"
)

// Store is the PostgreSQL implementation.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL. password, when not empty, overrides the one in
// the URL (NEXOSTREAM_DATABASE_PASSWORD_FILE).
func Open(ctx context.Context, url, password string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	if password != "" {
		cfg.ConnConfig.Password = password
	}
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Pool exposes the pool (migrations).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *Store) Close()                         { s.pool.Close() }

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return store.ErrConflict
		case "23503":
			if strings.Contains(pe.ConstraintName, "profile") {
				return store.ErrInUse
			}
			return store.ErrNotFound
		}
	}
	return err
}

// ---- channels ----

func (s *Store) ListChannels(ctx context.Context, f store.ChannelFilter) ([]model.Channel, error) {
	q := `SELECT id, name, group_name, enabled, mode, failover, auth, epg, overlay, revision, created_at, updated_at
	      FROM channels WHERE id > $1`
	args := []any{f.After}
	if f.Group != "" {
		args = append(args, f.Group)
		q += fmt.Sprintf(" AND group_name = $%d", len(args))
	}
	if f.Enabled != nil {
		args = append(args, *f.Enabled)
		q += fmt.Sprintf(" AND enabled = $%d", len(args))
	}
	if f.Search != "" {
		args = append(args, "%"+strings.ToLower(f.Search)+"%")
		q += fmt.Sprintf(" AND (lower(id) LIKE $%d OR lower(name) LIKE $%d)", len(args), len(args))
	}
	args = append(args, store.ClampLimit(f.Limit))
	q += fmt.Sprintf(" ORDER BY id LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	chs, err := scanChannels(rows)
	if err != nil {
		return nil, err
	}
	if err := s.loadRelations(ctx, chs); err != nil {
		return nil, err
	}
	return chs, nil
}

func scanChannels(rows pgx.Rows) ([]model.Channel, error) {
	defer rows.Close()
	out := []model.Channel{}
	for rows.Next() {
		var c model.Channel
		var fo, auth, epg, overlay []byte
		var mode string
		if err := rows.Scan(&c.ID, &c.Name, &c.Group, &c.Enabled, &mode, &fo, &auth, &epg, &overlay, &c.Revision, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Mode = model.ProcessingMode(mode)
		_ = json.Unmarshal(fo, &c.Failover)
		_ = json.Unmarshal(auth, &c.Auth)
		_ = json.Unmarshal(epg, &c.EPG)
		_ = json.Unmarshal(overlay, &c.Overlay)
		c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
		c.Profiles, c.Sources = []string{}, []model.Source{}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) loadRelations(ctx context.Context, chs []model.Channel) error {
	if len(chs) == 0 {
		return nil
	}
	ids := make([]string, len(chs))
	idx := map[string]int{}
	for i, c := range chs {
		ids[i] = c.ID
		idx[c.ID] = i
	}
	rows, err := s.pool.Query(ctx, `SELECT channel_id, id, priority, url, interface FROM channel_sources
	    WHERE channel_id = ANY($1) ORDER BY channel_id, priority`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid string
		var src model.Source
		if err := rows.Scan(&cid, &src.ID, &src.Priority, &src.URL, &src.Interface); err != nil {
			rows.Close()
			return err
		}
		chs[idx[cid]].Sources = append(chs[idx[cid]].Sources, src)
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT channel_id, profile_id FROM channel_profiles WHERE channel_id = ANY($1) ORDER BY channel_id, position`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, pid string
		if err := rows.Scan(&cid, &pid); err != nil {
			rows.Close()
			return err
		}
		chs[idx[cid]].Profiles = append(chs[idx[cid]].Profiles, pid)
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT o.channel_id, o.hls, o.mpegts_http, o.mpegts_udp, o.dash, o.push, o.drm_enabled, o.drm_method, o.drm_key_rotation_minutes,
	    COALESCE(d.enabled,false), COALESCE(d.retention_hours,0), COALESCE(d.path,''), COALESCE(d.storage_id,''), COALESCE(d.all_qualities,false)
	    FROM outputs o LEFT JOIN dvr_policies d ON d.channel_id = o.channel_id WHERE o.channel_id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid string
		var o model.Outputs
		var d model.DVRConfig
		var p model.DRMConfig
		var push []byte
		if err := rows.Scan(&cid, &o.HLS, &o.MPEGTSHTTP, &o.MPEGTSUDP, &o.DASH, &push, &p.Enabled, &p.Method, &p.KeyRotationMinutes,
			&d.Enabled, &d.RetentionHours, &d.Path, &d.Storage, &d.AllQualities); err != nil {
			return err
		}
		_ = json.Unmarshal(push, &o.Push)
		chs[idx[cid]].Outputs, chs[idx[cid]].DVR, chs[idx[cid]].DRM = o, d, p
	}
	return rows.Err()
}

func (s *Store) GetChannel(ctx context.Context, id string) (model.Channel, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, group_name, enabled, mode, failover, auth, epg, overlay, revision, created_at, updated_at
	    FROM channels WHERE id = $1`, id)
	if err != nil {
		return model.Channel{}, err
	}
	chs, err := scanChannels(rows)
	if err != nil {
		return model.Channel{}, err
	}
	if len(chs) == 0 {
		return model.Channel{}, store.ErrNotFound
	}
	if err := s.loadRelations(ctx, chs); err != nil {
		return model.Channel{}, err
	}
	return chs[0], nil
}

func (s *Store) writeRelations(ctx context.Context, tx pgx.Tx, ch *model.Channel) error {
	for _, q := range []string{
		"DELETE FROM channel_sources WHERE channel_id = $1",
		"DELETE FROM channel_profiles WHERE channel_id = $1",
	} {
		if _, err := tx.Exec(ctx, q, ch.ID); err != nil {
			return err
		}
	}
	for _, src := range ch.Sources {
		if _, err := tx.Exec(ctx, `INSERT INTO channel_sources (channel_id, id, priority, url, interface) VALUES ($1,$2,$3,$4,$5)`,
			ch.ID, src.ID, src.Priority, src.URL, src.Interface); err != nil {
			return err
		}
	}
	for i, p := range ch.Profiles {
		if _, err := tx.Exec(ctx, `INSERT INTO channel_profiles (channel_id, profile_id, position) VALUES ($1,$2,$3)`, ch.ID, p, i); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23503" {
				return fmt.Errorf("profile %q: %w", p, store.ErrNotFound)
			}
			return err
		}
	}
	push, _ := json.Marshal(ch.Outputs.Push)
	if ch.Outputs.Push == nil {
		push = []byte("[]")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outputs (channel_id, hls, mpegts_http, mpegts_udp, dash, push, drm_enabled, drm_method, drm_key_rotation_minutes)
	    VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	    ON CONFLICT (channel_id) DO UPDATE SET hls = EXCLUDED.hls, mpegts_http = EXCLUDED.mpegts_http, mpegts_udp = EXCLUDED.mpegts_udp,
	      dash = EXCLUDED.dash, push = EXCLUDED.push,
	      drm_enabled = EXCLUDED.drm_enabled, drm_method = EXCLUDED.drm_method, drm_key_rotation_minutes = EXCLUDED.drm_key_rotation_minutes`,
		ch.ID, ch.Outputs.HLS, ch.Outputs.MPEGTSHTTP, ch.Outputs.MPEGTSUDP, ch.Outputs.DASH, push, ch.DRM.Enabled, ch.DRM.Method, ch.DRM.KeyRotationMinutes); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO dvr_policies (channel_id, enabled, retention_hours, path, storage_id, all_qualities) VALUES ($1,$2,$3,$4,NULLIF($5,''),$6)
	    ON CONFLICT (channel_id) DO UPDATE SET enabled = EXCLUDED.enabled, retention_hours = EXCLUDED.retention_hours,
	      path = EXCLUDED.path, storage_id = EXCLUDED.storage_id, all_qualities = EXCLUDED.all_qualities`,
		ch.ID, ch.DVR.Enabled, ch.DVR.RetentionHours, ch.DVR.Path, ch.DVR.Storage, ch.DVR.AllQualities)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23503" {
		return fmt.Errorf("dvr storage %q: %w", ch.DVR.Storage, store.ErrNotFound)
	}
	return err
}

func (s *Store) CreateChannel(ctx context.Context, ch *model.Channel) error {
	fo, _ := json.Marshal(ch.Failover)
	auth, _ := json.Marshal(ch.Auth)
	epg, _ := json.Marshal(ch.EPG)
	overlay, _ := json.Marshal(ch.Overlay)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO channels (id, name, group_name, enabled, mode, failover, auth, epg, overlay)
		    VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING revision, created_at, updated_at`,
			ch.ID, ch.Name, ch.Group, ch.Enabled, string(ch.Mode), fo, auth, epg, overlay).Scan(&ch.Revision, &ch.CreatedAt, &ch.UpdatedAt)
		if err != nil {
			return mapErr(err)
		}
		ch.CreatedAt, ch.UpdatedAt = ch.CreatedAt.UTC(), ch.UpdatedAt.UTC()
		return s.writeRelations(ctx, tx, ch)
	})
}

func (s *Store) UpdateChannel(ctx context.Context, ch *model.Channel) error {
	fo, _ := json.Marshal(ch.Failover)
	auth, _ := json.Marshal(ch.Auth)
	epg, _ := json.Marshal(ch.EPG)
	overlay, _ := json.Marshal(ch.Overlay)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE channels SET name=$2, group_name=$3, enabled=$4, mode=$5, failover=$6, auth=$7, epg=$8, overlay=$9,
		    revision = revision + 1, updated_at = now() WHERE id = $1 RETURNING revision, created_at, updated_at`,
			ch.ID, ch.Name, ch.Group, ch.Enabled, string(ch.Mode), fo, auth, epg, overlay).Scan(&ch.Revision, &ch.CreatedAt, &ch.UpdatedAt)
		if err != nil {
			return mapErr(err)
		}
		ch.CreatedAt, ch.UpdatedAt = ch.CreatedAt.UTC(), ch.UpdatedAt.UTC()
		return s.writeRelations(ctx, tx, ch)
	})
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) CountChannels(ctx context.Context) (int, int, error) {
	var total, enabled int
	err := s.pool.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE enabled) FROM channels").Scan(&total, &enabled)
	return total, enabled, err
}

// ---- profiles ----

func scanProfile(row pgx.Row) (model.TranscodeProfile, error) {
	var p model.TranscodeProfile
	var v, a []byte
	var enc string
	if err := row.Scan(&p.ID, &v, &a, &enc); err != nil {
		return p, mapErr(err)
	}
	_ = json.Unmarshal(v, &p.Video)
	_ = json.Unmarshal(a, &p.Audio)
	p.Encoder = model.Encoder(enc)
	return p, nil
}

func (s *Store) ListProfiles(ctx context.Context) ([]model.TranscodeProfile, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, video, audio, encoder FROM transcode_profiles ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TranscodeProfile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProfile(ctx context.Context, id string) (model.TranscodeProfile, error) {
	return scanProfile(s.pool.QueryRow(ctx, "SELECT id, video, audio, encoder FROM transcode_profiles WHERE id = $1", id))
}

func (s *Store) CreateProfile(ctx context.Context, p model.TranscodeProfile) error {
	v, _ := json.Marshal(p.Video)
	a, _ := json.Marshal(p.Audio)
	_, err := s.pool.Exec(ctx, "INSERT INTO transcode_profiles (id, video, audio, encoder) VALUES ($1,$2,$3,$4)", p.ID, v, a, string(p.Encoder))
	return mapErr(err)
}

func (s *Store) UpdateProfile(ctx context.Context, p model.TranscodeProfile) error {
	v, _ := json.Marshal(p.Video)
	a, _ := json.Marshal(p.Audio)
	tag, err := s.pool.Exec(ctx, "UPDATE transcode_profiles SET video=$2, audio=$3, encoder=$4, updated_at=now() WHERE id=$1", p.ID, v, a, string(p.Encoder))
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM transcode_profiles WHERE id = $1", id)
	if err != nil {
		var pe *pgconn.PgError
		// 23001 restrict_violation (ON DELETE RESTRICT), 23503 foreign_key_violation.
		if errors.As(err, &pe) && (pe.Code == "23001" || pe.Code == "23503") {
			return store.ErrInUse
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ---- workers ----

func (s *Store) UpsertWorker(ctx context.Context, w model.Worker) error {
	caps, _ := json.Marshal(w.Capabilities)
	res, _ := json.Marshal(w.Resources)
	_, err := s.pool.Exec(ctx, `INSERT INTO workers (id, state, draining, last_heartbeat_at, started_at, version, capabilities, resources, channels)
	    VALUES ($1,$2,false,$3,$4,$5,$6,$7,$8)
	    ON CONFLICT (id) DO UPDATE SET
	      state = CASE WHEN workers.draining AND EXCLUDED.state = 'ONLINE' THEN 'DRAINING' ELSE EXCLUDED.state END,
	      last_heartbeat_at = EXCLUDED.last_heartbeat_at, started_at = EXCLUDED.started_at, version = EXCLUDED.version,
	      capabilities = EXCLUDED.capabilities, resources = EXCLUDED.resources, channels = EXCLUDED.channels`,
		w.ID, string(w.State), w.LastHeartbeatAt, w.StartedAt, w.Version, caps, res, w.Channels)
	return err
}

func scanWorker(row pgx.Row) (model.Worker, error) {
	var w model.Worker
	var state string
	var caps, res []byte
	if err := row.Scan(&w.ID, &state, &w.Draining, &w.LastHeartbeatAt, &w.StartedAt, &w.Version, &caps, &res, &w.Channels); err != nil {
		return w, mapErr(err)
	}
	w.State = model.WorkerState(state)
	w.LastHeartbeatAt, w.StartedAt = w.LastHeartbeatAt.UTC(), w.StartedAt.UTC()
	_ = json.Unmarshal(caps, &w.Capabilities)
	_ = json.Unmarshal(res, &w.Resources)
	return w, nil
}

const workerCols = "id, state, draining, last_heartbeat_at, started_at, version, capabilities, resources, channels"

func (s *Store) ListWorkers(ctx context.Context) ([]model.Worker, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+workerCols+" FROM workers ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Worker{}
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) GetWorker(ctx context.Context, id string) (model.Worker, error) {
	return scanWorker(s.pool.QueryRow(ctx, "SELECT "+workerCols+" FROM workers WHERE id = $1", id))
}

func (s *Store) SetWorkerDraining(ctx context.Context, id string, draining bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE workers SET draining = $2,
	    state = CASE WHEN $2 AND state = 'ONLINE' THEN 'DRAINING' WHEN NOT $2 AND state = 'DRAINING' THEN 'ONLINE' ELSE state END
	    WHERE id = $1`, id, draining)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) SetWorkerState(ctx context.Context, id string, state model.WorkerState) error {
	tag, err := s.pool.Exec(ctx, "UPDATE workers SET state = $2 WHERE id = $1", id, string(state))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) ListAssignments(ctx context.Context) ([]model.Assignment, error) {
	rows, err := s.pool.Query(ctx, "SELECT channel_id, worker_id, since, reason FROM channel_assignments ORDER BY channel_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Assignment{}
	for rows.Next() {
		var a model.Assignment
		if err := rows.Scan(&a.ChannelID, &a.WorkerID, &a.Since, &a.Reason); err != nil {
			return nil, err
		}
		a.Since = a.Since.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SetAssignment(ctx context.Context, a model.Assignment) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO channel_assignments (channel_id, worker_id, since, reason) VALUES ($1,$2,$3,$4)
	    ON CONFLICT (channel_id) DO UPDATE SET worker_id = EXCLUDED.worker_id, since = EXCLUDED.since, reason = EXCLUDED.reason`,
		a.ChannelID, a.WorkerID, a.Since, a.Reason)
	return mapErr(err)
}

func (s *Store) DeleteAssignment(ctx context.Context, channelID string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM channel_assignments WHERE channel_id = $1", channelID)
	return err
}

// ---- events ----

func (s *Store) InsertEvent(ctx context.Context, ev model.Event) error {
	var details []byte
	if len(ev.Details) > 0 {
		details, _ = json.Marshal(ev.Details)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO events (id, type, at, channel_id, worker_id, from_state, to_state, reason, details)
	    VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (id) DO NOTHING`,
		ev.ID, ev.Type, ev.At, ev.ChannelID, ev.WorkerID, string(ev.From), string(ev.To), ev.Reason, details)
	return err
}

func (s *Store) ListEvents(ctx context.Context, f store.EventFilter) ([]store.StoredEvent, error) {
	q := "SELECT seq, id, type, at, channel_id, worker_id, from_state, to_state, reason, details FROM events WHERE true"
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.Before > 0 {
		add("seq < $%d", f.Before)
	}
	if f.ChannelID != "" {
		add("channel_id = $%d", f.ChannelID)
	}
	if f.WorkerID != "" {
		add("worker_id = $%d", f.WorkerID)
	}
	if f.Type != "" {
		add("type = $%d", f.Type)
	}
	if !f.From.IsZero() {
		add("at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("at <= $%d", f.To)
	}
	args = append(args, store.ClampLimit(f.Limit))
	q += fmt.Sprintf(" ORDER BY seq DESC LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.StoredEvent{}
	for rows.Next() {
		var e store.StoredEvent
		var from, to string
		var details []byte
		if err := rows.Scan(&e.Seq, &e.ID, &e.Type, &e.At, &e.ChannelID, &e.WorkerID, &from, &to, &e.Reason, &details); err != nil {
			return nil, err
		}
		e.At, e.From, e.To = e.At.UTC(), model.ChannelState(from), model.ChannelState(to)
		if len(details) > 0 {
			_ = json.Unmarshal(details, &e.Details)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- users and tokens ----

const userCols = "id, username, role, password_hash, disabled, created_at, updated_at, tokens_valid_after, failed_logins, locked_until"

func scanUser(row pgx.Row) (model.User, error) {
	var u model.User
	var role string
	var valid, locked *time.Time
	if err := row.Scan(&u.ID, &u.Username, &role, &u.PasswordHash, &u.Disabled, &u.CreatedAt, &u.UpdatedAt, &valid,
		&u.FailedLogins, &locked); err != nil {
		return u, mapErr(err)
	}
	if valid != nil {
		u.TokensValidAfter = valid.UTC()
	}
	if locked != nil {
		u.LockedUntil = locked.UTC()
	}
	u.Role = model.Role(role)
	u.CreatedAt, u.UpdatedAt = u.CreatedAt.UTC(), u.UpdatedAt.UTC()
	return u, nil
}

// ---- refresh tokens ----

func (s *Store) CreateRefreshToken(ctx context.Context, t model.RefreshToken) error {
	_, err := s.pool.Exec(ctx, "INSERT INTO refresh_tokens (id, user_id, family_id, created_at, expires_at) VALUES ($1,$2,$3,$4,$5)",
		t.ID, t.UserID, t.FamilyID, t.CreatedAt, t.ExpiresAt)
	return mapErr(err)
}

func (s *Store) UseRefreshToken(ctx context.Context, id string, at time.Time) (model.RefreshToken, bool, error) {
	var t model.RefreshToken
	// The first use marks the row; the WHERE makes it atomic across API instances.
	err := s.pool.QueryRow(ctx, `UPDATE refresh_tokens SET used_at = $2 WHERE id = $1 AND used_at IS NULL
	    RETURNING id, user_id, family_id, created_at, expires_at`, id, at).Scan(&t.ID, &t.UserID, &t.FamilyID, &t.CreatedAt, &t.ExpiresAt)
	if err == nil {
		t.CreatedAt, t.ExpiresAt = t.CreatedAt.UTC(), t.ExpiresAt.UTC()
		return t, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return t, false, mapErr(err)
	}
	// Not marked now: either it does not exist or it was already used.
	err = s.pool.QueryRow(ctx, "SELECT id, user_id, family_id, created_at, expires_at FROM refresh_tokens WHERE id = $1", id).
		Scan(&t.ID, &t.UserID, &t.FamilyID, &t.CreatedAt, &t.ExpiresAt)
	if err != nil {
		return t, false, mapErr(err)
	}
	t.CreatedAt, t.ExpiresAt = t.CreatedAt.UTC(), t.ExpiresAt.UTC()
	return t, true, nil
}

func (s *Store) RevokeRefreshFamily(ctx context.Context, familyID string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM refresh_tokens WHERE family_id = $1", familyID)
	return mapErr(err)
}

func (s *Store) RevokeUserRefreshTokens(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM refresh_tokens WHERE user_id = $1", userID)
	return mapErr(err)
}

func (s *Store) PruneRefreshTokens(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM refresh_tokens WHERE expires_at < $1", before)
	return mapErr(err)
}

// nullTime maps the zero time to NULL.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *Store) CreateUser(ctx context.Context, u model.User) error {
	_, err := s.pool.Exec(ctx, "INSERT INTO users ("+userCols+") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
		u.ID, u.Username, string(u.Role), u.PasswordHash, u.Disabled, u.CreatedAt, u.UpdatedAt, nullTime(u.TokensValidAfter),
		u.FailedLogins, nullTime(u.LockedUntil))
	return mapErr(err)
}

func (s *Store) UpdateUser(ctx context.Context, u model.User) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET role=$2, password_hash=$3, disabled=$4, updated_at=$5, tokens_valid_after=$6,
		  failed_logins=$7, locked_until=$8 WHERE id=$1`,
		u.ID, string(u.Role), u.PasswordHash, u.Disabled, u.UpdatedAt, nullTime(u.TokensValidAfter),
		u.FailedLogins, nullTime(u.LockedUntil))
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) GetUser(ctx context.Context, id string) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE id = $1", id))
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE username = $1", username))
}

func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+userCols+" FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, err
}

// tokenCols lists column names (no credentials).
const tokenCols = "id, name, prefix, hash, scopes, created_by, created_at, expires_at, revoked_at, last_used_at" //nolint:gosec // G101: column names, not a credential

func scanToken(row pgx.Row) (model.APIToken, error) {
	var t model.APIToken
	if err := row.Scan(&t.ID, &t.Name, &t.Prefix, &t.Hash, &t.Scopes, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt); err != nil {
		return t, mapErr(err)
	}
	t.CreatedAt = t.CreatedAt.UTC()
	return t, nil
}

func (s *Store) CreateAPIToken(ctx context.Context, t model.APIToken) error {
	_, err := s.pool.Exec(ctx, "INSERT INTO api_tokens ("+tokenCols+") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
		t.ID, t.Name, t.Prefix, t.Hash, t.Scopes, t.CreatedBy, t.CreatedAt, t.ExpiresAt, t.RevokedAt, t.LastUsedAt)
	return mapErr(err)
}

func (s *Store) GetAPITokenByHash(ctx context.Context, hash string) (model.APIToken, error) {
	return scanToken(s.pool.QueryRow(ctx, "SELECT "+tokenCols+" FROM api_tokens WHERE hash = $1", hash))
}

func (s *Store) ListAPITokens(ctx context.Context) ([]model.APIToken, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+tokenCols+" FROM api_tokens ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAPIToken(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, "UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1", id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) TouchAPIToken(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx, "UPDATE api_tokens SET last_used_at = $2 WHERE id = $1", id, at)
	return err
}

// ---- DVR storages ----

func scanStorage(row pgx.Row) (model.DVRStorage, error) {
	var v model.DVRStorage
	if err := row.Scan(&v.ID, &v.Mode, &v.Disks, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return v, mapErr(err)
	}
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	return v, nil
}

func (s *Store) ListDVRStorages(ctx context.Context) ([]model.DVRStorage, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, mode, disks, created_at, updated_at FROM dvr_storages ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DVRStorage{}
	for rows.Next() {
		v, err := scanStorage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetDVRStorage(ctx context.Context, id string) (model.DVRStorage, error) {
	return scanStorage(s.pool.QueryRow(ctx, "SELECT id, mode, disks, created_at, updated_at FROM dvr_storages WHERE id = $1", id))
}

func (s *Store) CreateDVRStorage(ctx context.Context, v model.DVRStorage) error {
	_, err := s.pool.Exec(ctx, "INSERT INTO dvr_storages (id, mode, disks) VALUES ($1,$2,$3)", v.ID, v.Mode, v.Disks)
	return mapErr(err)
}

func (s *Store) UpdateDVRStorage(ctx context.Context, v model.DVRStorage) error {
	tag, err := s.pool.Exec(ctx, "UPDATE dvr_storages SET mode=$2, disks=$3, updated_at=now() WHERE id=$1", v.ID, v.Mode, v.Disks)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteDVRStorage(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM dvr_storages WHERE id = $1", id)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "23001" || pe.Code == "23503") {
			return store.ErrInUse
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ---- settings ----

func (s *Store) GetSetting(ctx context.Context, key string) ([]byte, error) {
	var v []byte
	if err := s.pool.QueryRow(ctx, "SELECT value FROM settings WHERE key = $1", key).Scan(&v); err != nil {
		return nil, mapErr(err)
	}
	return v, nil
}

func (s *Store) PutSetting(ctx context.Context, key string, value []byte) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2)
	    ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

func (s *Store) ListSettings(ctx context.Context, prefix string) (map[string][]byte, error) {
	rows, err := s.pool.Query(ctx, "SELECT key, value FROM settings WHERE starts_with(key, $1)", prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ---- audit ----

func (s *Store) InsertAudit(ctx context.Context, e model.AuditEntry) error {
	var details []byte
	if len(e.Details) > 0 {
		details, _ = json.Marshal(e.Details)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_log (at, actor, actor_type, action, target, source_ip, request_id, details)
	    VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, e.At, e.Actor, e.ActorType, e.Action, e.Target, e.SourceIP, e.RequestID, details)
	return err
}

func (s *Store) ListAudit(ctx context.Context, f store.AuditFilter) ([]model.AuditEntry, error) {
	q := "SELECT id, at, actor, actor_type, action, target, source_ip, request_id, details FROM audit_log WHERE true"
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.Before > 0 {
		add("id < $%d", f.Before)
	}
	if f.Actor != "" {
		add("actor = $%d", f.Actor)
	}
	if f.Action != "" {
		add("action = $%d", f.Action)
	}
	if f.Target != "" {
		add("target = $%d", f.Target)
	}
	if !f.From.IsZero() {
		add("at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("at <= $%d", f.To)
	}
	args = append(args, store.ClampLimit(f.Limit))
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditEntry{}
	for rows.Next() {
		var e model.AuditEntry
		var details []byte
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.ActorType, &e.Action, &e.Target, &e.SourceIP, &e.RequestID, &details); err != nil {
			return nil, err
		}
		e.At = e.At.UTC()
		if len(details) > 0 {
			_ = json.Unmarshal(details, &e.Details)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

var _ store.Store = (*Store)(nil)

// ---- EPG ----

func (s *Store) ReplacePrograms(ctx context.Context, channelID string, progs []model.Program) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if len(progs) > 0 {
			from := progs[0].Start
			for _, p := range progs {
				if p.Start.Before(from) {
					from = p.Start
				}
			}
			if _, err := tx.Exec(ctx, "DELETE FROM epg_programs WHERE channel_id = $1 AND start_at >= $2", channelID, from); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, "DELETE FROM epg_programs WHERE channel_id = $1", channelID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM epg_programs WHERE channel_id = $1 AND stop_at < now() - interval '30 days'", channelID); err != nil {
			return err
		}
		rows := make([][]any, 0, len(progs))
		seen := map[int64]bool{}
		for _, p := range progs {
			if seen[p.Start.UnixNano()] {
				continue // one program per start time
			}
			seen[p.Start.UnixNano()] = true
			rows = append(rows, []any{channelID, p.Start.UTC(), p.Stop.UTC(), p.Title, p.Description, p.Category})
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"epg_programs"},
			[]string{"channel_id", "start_at", "stop_at", "title", "description", "category"}, pgx.CopyFromRows(rows))
		if err != nil {
			return mapErr(err)
		}
		return nil
	})
}

func (s *Store) ListPrograms(ctx context.Context, channelID string, from, to time.Time) ([]model.Program, error) {
	rows, err := s.pool.Query(ctx, `SELECT start_at, stop_at, title, description, category FROM epg_programs
	    WHERE channel_id = $1 AND stop_at > $2 AND start_at < $3 ORDER BY start_at LIMIT 5000`, channelID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Program{}
	for rows.Next() {
		p := model.Program{ChannelID: channelID}
		if err := rows.Scan(&p.Start, &p.Stop, &p.Title, &p.Description, &p.Category); err != nil {
			return nil, err
		}
		p.Start, p.Stop = p.Start.UTC(), p.Stop.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- leases ----

// AcquireLease uses the database clock, so controllers with skewed clocks
// still agree on who holds the lease.
func (s *Store) AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	var got string
	err := s.pool.QueryRow(ctx, `INSERT INTO leases (name, holder, expires_at) VALUES ($1, $2, now() + $3 * interval '1 millisecond')
	    ON CONFLICT (name) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
	    WHERE leases.holder = EXCLUDED.holder OR leases.expires_at < now()
	    RETURNING holder`, name, holder, ttl.Milliseconds()).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == holder, nil
}

func (s *Store) ReleaseLease(ctx context.Context, name, holder string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM leases WHERE name = $1 AND holder = $2", name, holder)
	return err
}

// ---- statistics ----

func (s *Store) InsertStats(ctx context.Context, at time.Time, values map[string]float64) error {
	if len(values) == 0 {
		return nil
	}
	at = at.UTC().Truncate(time.Minute)
	keys := make([]string, 0, len(values))
	vals := make([]float64, 0, len(values))
	for k, v := range values {
		keys = append(keys, k)
		vals = append(vals, v)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO stats (at, key, value) SELECT $1, k, v FROM unnest($2::text[], $3::float8[]) AS t(k, v)
	    ON CONFLICT (key, at) DO UPDATE SET value = EXCLUDED.value`, at, keys, vals)
	return err
}

func (s *Store) ListStats(ctx context.Context, keys []string, prefix string, from, to time.Time, step time.Duration) (map[string][]model.StatPoint, error) {
	if step < time.Minute {
		step = time.Minute
	}
	if keys == nil {
		keys = []string{}
	}
	sec := int64(step / time.Second)
	rows, err := s.pool.Query(ctx, `SELECT key, to_timestamp(floor(extract(epoch FROM at) / $5) * $5) AS b, avg(value)
	    FROM stats WHERE at >= $1 AND at < $2 AND (key = ANY($3) OR ($4 <> '' AND starts_with(key, $4)))
	    GROUP BY key, b ORDER BY key, b`, from, to, keys, prefix, sec)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]model.StatPoint{}
	for rows.Next() {
		var k string
		var p model.StatPoint
		if err := rows.Scan(&k, &p.At, &p.Value); err != nil {
			return nil, err
		}
		p.At = p.At.UTC()
		out[k] = append(out[k], p)
	}
	return out, rows.Err()
}

func (s *Store) TopStats(ctx context.Context, prefix string, from, to time.Time, limit int) ([]model.StatTotal, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `SELECT key, max(value), sum(value), count(*) FROM stats
	    WHERE at >= $1 AND at < $2 AND starts_with(key, $3) GROUP BY key ORDER BY sum(value) DESC, key LIMIT $4`, from, to, prefix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.StatTotal{}
	for rows.Next() {
		var t model.StatTotal
		if err := rows.Scan(&t.Key, &t.Max, &t.Sum, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) PruneStats(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM stats WHERE at < $1", before)
	return err
}

// ---- play session history ----

func (s *Store) InsertPlaySessions(ctx context.Context, ss []model.PlaySession) error {
	if len(ss) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, p := range ss {
		b.Queue(`INSERT INTO play_sessions (id, channel_id, worker_id, proto, ip, user_agent, user_id, token, started_at, last_seen, closed_at, bytes, requests, reason)
		    VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (id) DO NOTHING`,
			p.ID, p.ChannelID, p.WorkerID, p.Proto, p.IP, p.UserAgent, p.UserID, p.Token, p.StartedAt, p.LastSeen, p.ClosedAt, p.Bytes, p.Requests, p.Reason)
	}
	return s.pool.SendBatch(ctx, b).Close()
}

func (s *Store) ListPlaySessions(ctx context.Context, f store.SessionFilter) ([]model.PlaySession, error) {
	q := `SELECT id, channel_id, worker_id, proto, ip, user_agent, user_id, token, started_at, last_seen, closed_at, bytes, requests, reason
	      FROM play_sessions WHERE true`
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.ChannelID != "" {
		add("channel_id = $%d", f.ChannelID)
	}
	if f.UserID != "" {
		add("user_id = $%d", f.UserID)
	}
	if f.IP != "" {
		add("ip = $%d", f.IP)
	}
	if !f.From.IsZero() {
		add("closed_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("closed_at < $%d", f.To)
	}
	if ns, id, ok := strings.Cut(f.Before, "|"); ok {
		if n, err := strconv.ParseInt(ns, 10, 64); err == nil {
			args = append(args, time.Unix(0, n).UTC(), id)
			q += fmt.Sprintf(" AND (closed_at, id) < ($%d, $%d)", len(args)-1, len(args))
		}
	}
	args = append(args, store.ClampLimit(f.Limit))
	q += fmt.Sprintf(" ORDER BY closed_at DESC, id DESC LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PlaySession{}
	for rows.Next() {
		var p model.PlaySession
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.WorkerID, &p.Proto, &p.IP, &p.UserAgent, &p.UserID, &p.Token,
			&p.StartedAt, &p.LastSeen, &p.ClosedAt, &p.Bytes, &p.Requests, &p.Reason); err != nil {
			return nil, err
		}
		p.StartedAt, p.LastSeen, p.ClosedAt = p.StartedAt.UTC(), p.LastSeen.UTC(), p.ClosedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PrunePlaySessions(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM play_sessions WHERE closed_at < $1", before)
	return err
}
