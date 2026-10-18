package gitsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"lodan/internal/memory"
)

// applier applies the lines of batch files inside one import transaction.
type applier struct {
	tx             pgx.Tx
	topics         map[string]int32 // slug -> id, cache for this transaction
	contradictions []Contradiction
}

// local is the state of a local memory that an incoming line refers to.
type local struct {
	id        int64
	syncID    string
	rev       string
	base      string
	dirty     bool
	status    string
	key       string
	title     string
	content   string
	kind      string
	sessionID string
	occurred  time.Time
	created   time.Time
	updated   time.Time
}

// newer reports whether version (at, tie) is more recent than (bt, btie). The tie-break on
// the identifiers makes every machine take the same decision.
func newer(at time.Time, tie string, bt time.Time, btie string) bool {
	if !at.Equal(bt) {
		return at.After(bt)
	}
	return tie > btie
}

// resolve returns the local sync_id an incoming sync_id refers to (itself or its alias).
func (a *applier) resolve(ctx context.Context, syncID string) (string, error) {
	var target string
	err := a.tx.QueryRow(ctx, `SELECT sync_id::text FROM sync_aliases WHERE alias = $1::uuid`, syncID).Scan(&target)
	switch {
	case err == nil:
		return target, nil
	case errors.Is(err, pgx.ErrNoRows):
		return syncID, nil
	default:
		return "", fmt.Errorf("no se pudo resolver el registro %s: %w", syncID, err)
	}
}

func (a *applier) tombstoned(ctx context.Context, ids ...string) (bool, error) {
	var found bool
	err := a.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sync_tombstones WHERE sync_id = ANY($1::text[]::uuid[]))`, ids).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("no se pudieron comprobar los borrados: %w", err)
	}
	return found, nil
}

const localColumns = `id, sync_id::text, sync_rev::text, coalesce(sync_base::text, ''), sync_dirty, status::text,
	coalesce(key, ''), title, content, kind::text, coalesce(session_id, ''), occurred_at, created_at, updated_at`

func scanLocal(row pgx.Row) (*local, error) {
	var l local
	err := row.Scan(&l.id, &l.syncID, &l.rev, &l.base, &l.dirty, &l.status, &l.key, &l.title, &l.content, &l.kind,
		&l.sessionID, &l.occurred, &l.created, &l.updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer el registro local: %w", err)
	}
	return &l, nil
}

func (a *applier) localBySync(ctx context.Context, syncID string) (*local, error) {
	return scanLocal(a.tx.QueryRow(ctx, `SELECT `+localColumns+` FROM memories WHERE sync_id = $1::uuid FOR UPDATE`, syncID))
}

// activeByHash and activeByKey return the active record (other than exceptID) with the same content hash or key.
func (a *applier) activeByHash(ctx context.Context, hash []byte, exceptID int64) (*local, error) {
	return scanLocal(a.tx.QueryRow(ctx, `SELECT `+localColumns+` FROM memories
		WHERE content_hash = $1 AND status = 'active' AND id <> $2 FOR UPDATE`, hash, exceptID))
}

func (a *applier) activeByKey(ctx context.Context, key string, exceptID int64) (*local, error) {
	return scanLocal(a.tx.QueryRow(ctx, `SELECT `+localColumns+` FROM memories
		WHERE key = $1 AND status = 'active' AND id <> $2 FOR UPDATE`, key, exceptID))
}

// knownRev reports whether the record has already had version rev on this machine.
func (a *applier) knownRev(ctx context.Context, syncID, rev string) (bool, error) {
	var found bool
	err := a.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sync_revs WHERE sync_id = $1::uuid AND rev = $2::uuid)`, syncID, rev).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("no se pudieron leer las versiones del registro: %w", err)
	}
	return found, nil
}

func (a *applier) addRev(ctx context.Context, syncID, rev string) error {
	_, err := a.tx.Exec(ctx, `INSERT INTO sync_revs (sync_id, rev) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, syncID, rev)
	if err != nil {
		return fmt.Errorf("no se pudo guardar la versión del registro: %w", err)
	}
	return nil
}

// topicIDs returns the ids of the topics with these slugs, creating the missing ones (their
// embedding is computed later by the maintenance worker).
func (a *applier) topicIDs(ctx context.Context, slugs []string) ([]int32, error) {
	ids := make([]int32, 0, len(slugs))
	for _, slug := range slugs {
		if id, ok := a.topics[slug]; ok {
			ids = append(ids, id)
			continue
		}
		var id int32
		if err := a.tx.QueryRow(ctx, `INSERT INTO topics (slug) VALUES ($1)
			ON CONFLICT (slug) DO UPDATE SET last_used_at = greatest(topics.last_used_at, now())
			RETURNING id`, slug).Scan(&id); err != nil {
			return nil, fmt.Errorf("no se pudo crear el tema %q: %w", slug, err)
		}
		a.topics[slug] = id
		ids = append(ids, id)
	}
	return ids, nil
}

// setTopics replaces the topics of a memory.
func (a *applier) setTopics(ctx context.Context, memoryID int64, slugs []string, kind, status string, ts time.Time) error {
	ids, err := a.topicIDs(ctx, slugs)
	if err != nil {
		return err
	}
	if _, err := a.tx.Exec(ctx, `DELETE FROM memory_topics WHERE memory_id = $1 AND NOT (topic_id = ANY($2::int[]))`, memoryID, ids); err != nil {
		return fmt.Errorf("no se pudieron actualizar los temas: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	_, err = a.tx.Exec(ctx, `INSERT INTO memory_topics (topic_id, memory_id, kind, active, ts)
		SELECT unnest($1::int[]), $2::bigint, $3::text::memory_kind, $4::boolean, $5::timestamptz
		ON CONFLICT (topic_id, memory_id) DO UPDATE SET kind = EXCLUDED.kind, active = EXCLUDED.active, ts = EXCLUDED.ts`,
		ids, memoryID, kind, status == string(memory.StatusActive), ts)
	if err != nil {
		return fmt.Errorf("no se pudieron guardar los temas: %w", err)
	}
	return nil
}

func (a *applier) sessionOrNil(ctx context.Context, id string) (any, error) {
	if id == "" {
		return nil, nil
	}
	var found bool
	if err := a.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE id = $1)`, id).Scan(&found); err != nil {
		return nil, fmt.Errorf("no se pudo comprobar la sesión: %w", err)
	}
	if !found {
		return nil, nil
	}
	return id, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (a *applier) relate(ctx context.Context, source, target int64, kind string) error {
	_, err := a.tx.Exec(ctx, `INSERT INTO memory_relations (source_id, target_id, kind, state)
		VALUES ($1, $2, $3::text::relation_kind, 'confirmed')
		ON CONFLICT (source_id, target_id, kind) DO UPDATE SET state = 'confirmed'`, source, target, kind)
	if err != nil {
		return fmt.Errorf("no se pudo crear la relación %s: %w", kind, err)
	}
	return nil
}

// applySession joins a session by id: the most recent activity decides the end and summary.
func (a *applier) applySession(ctx context.Context, l sessionLine) error {
	var ended any
	if l.EndedAt != nil {
		ended = *l.EndedAt
	}
	_, err := a.tx.Exec(ctx, `INSERT INTO sessions AS s (id, client, started_at, last_activity_at, ended_at, summary)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
		  started_at = least(s.started_at, EXCLUDED.started_at),
		  last_activity_at = greatest(s.last_activity_at, EXCLUDED.last_activity_at),
		  ended_at = CASE WHEN EXCLUDED.last_activity_at >= s.last_activity_at
		                  THEN coalesce(EXCLUDED.ended_at, s.ended_at) ELSE coalesce(s.ended_at, EXCLUDED.ended_at) END,
		  summary = CASE WHEN EXCLUDED.last_activity_at >= s.last_activity_at
		                 THEN coalesce(nullif(EXCLUDED.summary, ''), s.summary) ELSE coalesce(nullif(s.summary, ''), EXCLUDED.summary) END`,
		l.ID, l.Client, l.StartedAt, l.LastActivityAt, ended, nullable(l.Summary))
	if err != nil {
		return fmt.Errorf("no se pudo importar la sesión %s: %w", l.ID, err)
	}
	ids, err := a.topicIDs(ctx, l.Topics)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err := a.tx.Exec(ctx, `INSERT INTO session_topics (session_id, topic_id)
			SELECT $1::text, unnest($2::int[]) ON CONFLICT DO NOTHING`, l.ID, ids); err != nil {
			return fmt.Errorf("no se pudieron importar los temas de la sesión %s: %w", l.ID, err)
		}
	}
	return nil
}

// applyMemory merges one incoming memory (rules S6 a-f).
func (a *applier) applyMemory(ctx context.Context, in memoryLine) error {
	syncID, err := a.resolve(ctx, in.SyncID)
	if err != nil {
		return err
	}
	if gone, err := a.tombstoned(ctx, in.SyncID, syncID); err != nil || gone {
		return err
	}
	l, err := a.localBySync(ctx, syncID)
	if err != nil {
		return err
	}
	if l == nil {
		return a.insertMemory(ctx, in)
	}
	return a.mergeMemory(ctx, l, in)
}

// insertMemory creates a record that does not exist locally.
func (a *applier) insertMemory(ctx context.Context, in memoryLine) error {
	hash := memory.ContentHash(in.Content)
	if in.Status != string(memory.StatusActive) {
		_, err := a.insertRow(ctx, in, hash, in.Key)
		return err
	}
	// The same content already exists here as another record: both are the same record.
	same, err := a.activeByHash(ctx, hash, 0)
	if err != nil {
		return err
	}
	if same != nil {
		return a.alias(ctx, in, same)
	}
	var other *local
	if in.Key != "" {
		if other, err = a.activeByKey(ctx, in.Key, 0); err != nil {
			return err
		}
	}
	if other == nil {
		_, err := a.insertRow(ctx, in, hash, in.Key)
		return err
	}
	// Two active records with the same key: the most recent keeps it.
	if newer(in.UpdatedAt, in.SyncID, other.updated, other.syncID) {
		if _, err := a.tx.Exec(ctx, `UPDATE memories SET key = NULL WHERE id = $1`, other.id); err != nil {
			return fmt.Errorf("no se pudo quitar la clave del registro #%d: %w", other.id, err)
		}
		id, err := a.insertRow(ctx, in, hash, in.Key)
		if err != nil {
			return err
		}
		return a.contradict(ctx, id, other.id, in.Key)
	}
	id, err := a.insertRow(ctx, in, hash, "")
	if err != nil {
		return err
	}
	return a.contradict(ctx, other.id, id, in.Key)
}

func (a *applier) contradict(ctx context.Context, kept, other int64, key string) error {
	if err := a.relate(ctx, kept, other, "contradicts"); err != nil {
		return err
	}
	a.contradictions = append(a.contradictions, Contradiction{Key: key, KeptID: kept, OtherID: other})
	return nil
}

func (a *applier) insertRow(ctx context.Context, in memoryLine, hash []byte, key string) (int64, error) {
	sid, err := a.sessionOrNil(ctx, in.SessionID)
	if err != nil {
		return 0, err
	}
	var id int64
	err = a.tx.QueryRow(ctx, `INSERT INTO memories
		(sync_id, sync_rev, sync_base, kind, status, title, content, key, content_hash, session_id, occurred_at, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $2::uuid, $3::text::memory_kind, $4::text::memory_status, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id`,
		in.SyncID, in.Rev, in.Kind, in.Status, in.Title, in.Content, nullable(key), hash, sid,
		in.OccurredAt, in.CreatedAt, in.UpdatedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("no se pudo importar el registro %s: %w", in.SyncID, err)
	}
	if err := a.setTopics(ctx, id, in.Topics, in.Kind, in.Status, in.OccurredAt); err != nil {
		return 0, err
	}
	return id, nil
}

// alias records that the incoming record is the local one with the same content.
func (a *applier) alias(ctx context.Context, in memoryLine, same *local) error {
	if _, err := a.tx.Exec(ctx, `INSERT INTO sync_aliases (alias, sync_id) VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (alias) DO UPDATE SET sync_id = EXCLUDED.sync_id`, in.SyncID, same.syncID); err != nil {
		return fmt.Errorf("no se pudo unir el registro %s con #%d: %w", in.SyncID, same.id, err)
	}
	if err := a.addRev(ctx, same.syncID, in.Rev); err != nil {
		return err
	}
	if _, err := a.tx.Exec(ctx, `UPDATE memories SET sync_base = $2::uuid WHERE id = $1`, same.id, in.Rev); err != nil {
		return fmt.Errorf("no se pudo unir el registro %s con #%d: %w", in.SyncID, same.id, err)
	}
	return nil
}

// mergeMemory merges an incoming version into an existing local record.
func (a *applier) mergeMemory(ctx context.Context, l *local, in memoryLine) error {
	known, err := a.knownRev(ctx, l.syncID, in.Rev)
	if err != nil {
		return err
	}

	// Status only moves forward: a retired record is never reactivated.
	status := l.status
	if in.Status != string(memory.StatusActive) &&
		(l.status == string(memory.StatusActive) || (l.status != in.Status && in.UpdatedAt.After(l.updated))) {
		status = in.Status
	}

	switch {
	case known:
		// The incoming content is one this record already had: only status and topics.
		updated := l.updated
		if in.UpdatedAt.After(updated) {
			updated = in.UpdatedAt
		}
		if _, err := a.tx.Exec(ctx, `UPDATE memories SET status = $2::text::memory_status, updated_at = $3 WHERE id = $1`,
			l.id, status, updated); err != nil {
			return fmt.Errorf("no se pudo actualizar el registro #%d: %w", l.id, err)
		}
		if in.UpdatedAt.After(l.updated) {
			return a.setTopics(ctx, l.id, in.Topics, l.kind, status, l.occurred)
		}
		return a.syncTopicsActive(ctx, l.id, status)

	case in.Base != "" && !l.dirty && (in.Base == l.rev || in.Base == l.base):
		// Edited on top of the version this machine has: it simply replaces it.
		return a.applyContent(ctx, l, in, status)

	default:
		// Both machines edited the record without seeing each other's edit: the most recent
		// wins and the other version stays as a superseded copy.
		if newer(in.UpdatedAt, in.Rev, l.updated, l.rev) {
			copyID, err := a.insertCopy(ctx, l.syncID, l.rev, l.title, l.content, l.kind, l.sessionID, l.occurred, l.created, l.updated, l.id)
			if err != nil {
				return err
			}
			if err := a.applyContent(ctx, l, in, status); err != nil {
				return err
			}
			if err := a.relate(ctx, l.id, copyID, "supersedes"); err != nil {
				return err
			}
			// A local edit that was never exported survives only in this copy: the other
			// machine has never seen it, so the copy and its relation must be exported.
			if l.dirty {
				return a.markLocal(ctx, copyID, l.id)
			}
			return nil
		}
		copyID, err := a.insertCopy(ctx, l.syncID, in.Rev, in.Title, in.Content, in.Kind, in.SessionID, in.OccurredAt, in.CreatedAt, in.UpdatedAt, l.id)
		if err != nil {
			return err
		}
		if err := a.addRev(ctx, l.syncID, in.Rev); err != nil {
			return err
		}
		if status != l.status {
			if _, err := a.tx.Exec(ctx, `UPDATE memories SET status = $2::text::memory_status WHERE id = $1`, l.id, status); err != nil {
				return fmt.Errorf("no se pudo actualizar el registro #%d: %w", l.id, err)
			}
			if err := a.syncTopicsActive(ctx, l.id, status); err != nil {
				return err
			}
		}
		return a.relate(ctx, l.id, copyID, "supersedes")
	}
}

// markLocal marks a copy and its supersedes relation as local changes, so the next export
// sends them. Setting sync_xid directly does not fire the sync triggers.
func (a *applier) markLocal(ctx context.Context, copyID, winnerID int64) error {
	if _, err := a.tx.Exec(ctx, `UPDATE memories SET sync_xid = pg_current_xact_id() WHERE id = $1`, copyID); err != nil {
		return fmt.Errorf("no se pudo marcar la versión anterior para enviarla: %w", err)
	}
	if _, err := a.tx.Exec(ctx, `UPDATE memory_relations SET sync_xid = pg_current_xact_id()
		WHERE source_id = $1 AND target_id = $2 AND kind = 'supersedes'`, winnerID, copyID); err != nil {
		return fmt.Errorf("no se pudo marcar la relación con la versión anterior: %w", err)
	}
	return nil
}

// applyContent replaces the content of a local record with the incoming version.
func (a *applier) applyContent(ctx context.Context, l *local, in memoryLine, status string) error {
	hash := memory.ContentHash(in.Content)
	key := in.Key
	var lostKey *local
	if status == string(memory.StatusActive) {
		if same, err := a.activeByHash(ctx, hash, l.id); err != nil {
			return err
		} else if same != nil {
			// Another active record already says the same: this one is retired in its favour.
			status = string(memory.StatusSuperseded)
			if err := a.relate(ctx, same.id, l.id, "supersedes"); err != nil {
				return err
			}
		}
	}
	if status == string(memory.StatusActive) && key != "" {
		other, err := a.activeByKey(ctx, key, l.id)
		if err != nil {
			return err
		}
		if other != nil {
			if newer(in.UpdatedAt, l.syncID, other.updated, other.syncID) {
				if _, err := a.tx.Exec(ctx, `UPDATE memories SET key = NULL WHERE id = $1`, other.id); err != nil {
					return fmt.Errorf("no se pudo quitar la clave del registro #%d: %w", other.id, err)
				}
				lostKey = other
			} else {
				key = ""
				if err := a.contradict(ctx, other.id, l.id, in.Key); err != nil {
					return err
				}
			}
		}
	}
	sid, err := a.sessionOrNil(ctx, in.SessionID)
	if err != nil {
		return err
	}
	if _, err := a.tx.Exec(ctx, `UPDATE memories SET
		title = $2, content = $3, content_hash = $4, kind = $5::text::memory_kind, key = $6,
		status = $7::text::memory_status, occurred_at = $8, updated_at = $9, session_id = coalesce($10, session_id),
		sync_rev = $11::uuid, sync_base = $11::uuid, sync_dirty = false,
		embedding = CASE WHEN content = $3 AND title = $2 THEN embedding END,
		embedding_model = CASE WHEN content = $3 AND title = $2 THEN embedding_model END
		WHERE id = $1`,
		l.id, in.Title, in.Content, hash, in.Kind, nullable(key), status, in.OccurredAt, in.UpdatedAt, sid, in.Rev); err != nil {
		return fmt.Errorf("no se pudo actualizar el registro #%d: %w", l.id, err)
	}
	if err := a.setTopics(ctx, l.id, in.Topics, in.Kind, status, in.OccurredAt); err != nil {
		return err
	}
	if lostKey != nil {
		return a.contradict(ctx, l.id, lostKey.id, key)
	}
	return nil
}

// syncTopicsActive keeps memory_topics.active in line with the record status.
func (a *applier) syncTopicsActive(ctx context.Context, id int64, status string) error {
	if _, err := a.tx.Exec(ctx, `UPDATE memory_topics SET active = $2 WHERE memory_id = $1`, id, status == string(memory.StatusActive)); err != nil {
		return fmt.Errorf("no se pudieron actualizar los temas del registro #%d: %w", id, err)
	}
	return nil
}

// insertCopy keeps a losing version as a superseded record with the topics of the record.
// Its sync_id is derived from the record and the losing version, so every machine that
// resolves the same conflict creates the same copy, and later changes to it converge.
func (a *applier) insertCopy(ctx context.Context, recordSyncID, losingRev, title, content, kind, sessionID string,
	occurred, created, updated time.Time, of int64) (int64, error) {
	sid, err := a.sessionOrNil(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	var id int64
	err = a.tx.QueryRow(ctx, `INSERT INTO memories
		(sync_id, sync_rev, sync_base, kind, status, title, content, content_hash, session_id, occurred_at, created_at, updated_at)
		VALUES (md5($9 || ':' || $10)::uuid, $10::uuid, $10::uuid, $1::text::memory_kind, 'superseded', $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (sync_id) DO NOTHING RETURNING id`,
		kind, title, content, memory.ContentHash(content), sid, occurred, created, updated, recordSyncID, losingRev).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// This conflict was already resolved here (or the copy arrived from the other machine).
		err = a.tx.QueryRow(ctx, `SELECT id FROM memories WHERE sync_id = md5($1 || ':' || $2)::uuid`, recordSyncID, losingRev).Scan(&id)
	}
	if err != nil {
		return 0, fmt.Errorf("no se pudo guardar la versión anterior: %w", err)
	}
	if _, err := a.tx.Exec(ctx, `INSERT INTO memory_topics (topic_id, memory_id, kind, active, ts)
		SELECT topic_id, $2, $3::text::memory_kind, false, $4 FROM memory_topics WHERE memory_id = $1`,
		of, id, kind, occurred); err != nil {
		return 0, fmt.Errorf("no se pudieron copiar los temas de la versión anterior: %w", err)
	}
	return id, nil
}

// applyRelation joins a relation by (source, target, kind); the most recent change decides its
// state. If a record has not arrived yet, the relation waits in sync_pending_relations.
func (a *applier) applyRelation(ctx context.Context, in relationLine) error {
	src, err := a.resolve(ctx, in.Source)
	if err != nil {
		return err
	}
	dst, err := a.resolve(ctx, in.Target)
	if err != nil {
		return err
	}
	if gone, err := a.tombstoned(ctx, in.Source, in.Target, src, dst); err != nil || gone {
		return err
	}
	var srcID, dstID int64
	err = a.tx.QueryRow(ctx, `SELECT (SELECT id FROM memories WHERE sync_id = $1::uuid), (SELECT id FROM memories WHERE sync_id = $2::uuid)`,
		src, dst).Scan(nullInt(&srcID), nullInt(&dstID))
	if err != nil {
		return fmt.Errorf("no se pudieron buscar los registros de la relación: %w", err)
	}
	if srcID == 0 || dstID == 0 {
		// sync_pending_relations has no verdict/confidence columns yet (a future
		// migration could add them); the verdict and confidence are applied when
		// the missing record arrives and the pending row is replayed.
		_, err := a.tx.Exec(ctx, `INSERT INTO sync_pending_relations (source, target, kind, state, created_at, updated_at)
			VALUES ($1::uuid, $2::uuid, $3::text::relation_kind, $4::text::relation_state, $5, $6)
			ON CONFLICT (source, target, kind) DO UPDATE SET state = EXCLUDED.state,
				updated_at = EXCLUDED.updated_at
			WHERE sync_pending_relations.updated_at < EXCLUDED.updated_at`,
			in.Source, in.Target, in.Kind, in.State, in.CreatedAt, in.UpdatedAt)
		if err != nil {
			return fmt.Errorf("no se pudo guardar la relación pendiente: %w", err)
		}
		return nil
	}
	_, err = a.tx.Exec(ctx, `INSERT INTO memory_relations AS r (source_id, target_id, kind, state, verdict, confidence, created_at, updated_at)
		VALUES ($1, $2, $3::text::relation_kind, $4::text::relation_state, $5, $6, $7, $8)
		ON CONFLICT (source_id, target_id, kind) DO UPDATE SET state = EXCLUDED.state, verdict = EXCLUDED.verdict,
			confidence = EXCLUDED.confidence, updated_at = EXCLUDED.updated_at
		WHERE r.updated_at < EXCLUDED.updated_at`,
		srcID, dstID, in.Kind, in.State, nullStr(in.Verdict), in.Confidence, in.CreatedAt, in.UpdatedAt)
	if err != nil {
		return fmt.Errorf("no se pudo importar la relación: %w", err)
	}
	return nil
}

// nullStr returns nil when s is empty so the column stores NULL instead of ''.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// applyAlias upserts the alias row, with last-write-wins by updated_at. When
// the alias is confirmed it retags the local records: every memory_topics
// pointing to the alias's topic gains the canonical's topic (creating it on
// conflict), then the alias's topic is deactivated on those records.
func (a *applier) applyAlias(ctx context.Context, in aliasLine) error {
	// Last-write-wins on (alias, updated_at) for the whole alias: any older
	// local row with the same alias is replaced, the UNIQUE INDEX on
	// tag_aliases_confirmed (alias WHERE state='confirmed') cannot fire.
	rows, err := a.tx.Query(ctx, `SELECT alias, canonical, state::text, updated_at
		FROM tag_aliases WHERE alias = $1 FOR UPDATE`, in.Alias)
	if err != nil {
		return fmt.Errorf("no se pudo bloquear el alias %s: %w", in.Alias, err)
	}
	var newest aliasLine
	newest.UpdatedAt = time.Time{}
	for rows.Next() {
		var l aliasLine
		if err := rows.Scan(&l.Alias, &l.Canonical, &l.State, &l.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		if l.UpdatedAt.After(newest.UpdatedAt) {
			newest = l
		}
	}
	rows.Close()
	if !newest.UpdatedAt.IsZero() && !in.UpdatedAt.After(newest.UpdatedAt) {
		return nil
	}
	if _, err := a.tx.Exec(ctx, `DELETE FROM tag_aliases WHERE alias = $1`, in.Alias); err != nil {
		return fmt.Errorf("no se pudo limpiar el alias %s: %w", in.Alias, err)
	}
	if _, err := a.tx.Exec(ctx, `INSERT INTO tag_aliases (alias, canonical, state, updated_at)
		VALUES ($1, $2, $3::text, $4)`, in.Alias, in.Canonical, in.State, in.UpdatedAt); err != nil {
		return fmt.Errorf("no se pudo guardar el alias %s → %s: %w", in.Alias, in.Canonical, err)
	}
	if in.State != "confirmed" {
		return nil
	}
	aliasID, err := a.topicID(ctx, in.Alias)
	if err != nil {
		return err
	}
	canonicalID, err := a.topicID(ctx, in.Canonical)
	if err != nil {
		return err
	}
	// Ensure every record carrying the alias gains the canonical (no duplicates).
	if _, err := a.tx.Exec(ctx, `INSERT INTO memory_topics (memory_id, topic_id, kind, active, ts)
		SELECT mt.memory_id, $2, mt.kind, true, now()
		FROM memory_topics mt
		WHERE mt.topic_id = $1 AND mt.active
		ON CONFLICT (memory_id, topic_id) DO UPDATE SET active = true, ts = EXCLUDED.ts`,
		aliasID, canonicalID); err != nil {
		return fmt.Errorf("no se pudo reetiquetar con %s: %w", in.Canonical, err)
	}
	// Deactivate the alias on every record; the canonical now does the work.
	if _, err := a.tx.Exec(ctx, `UPDATE memory_topics SET active = false WHERE topic_id = $1 AND active`, aliasID); err != nil {
		return fmt.Errorf("no se pudo desactivar el alias %s: %w", in.Alias, err)
	}
	return nil
}

// topicID returns the id of the topic with the given slug, creating it when
// missing (its embedding is computed later by the maintenance worker). It
// mirrors the topicIDs cache used by memory apply.
func (a *applier) topicID(ctx context.Context, slug string) (int32, error) {
	if slug == "" {
		return 0, fmt.Errorf("el slug no puede estar vacío")
	}
	if id, ok := a.topics[slug]; ok {
		return id, nil
	}
	var id int32
	if err := a.tx.QueryRow(ctx, `INSERT INTO topics (slug) VALUES ($1)
		ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
		RETURNING id`, slug).Scan(&id); err != nil {
		return 0, fmt.Errorf("no se pudo preparar el tema %s: %w", slug, err)
	}
	if a.topics == nil {
		a.topics = map[string]int32{}
	}
	a.topics[slug] = id
	return id, nil
}

// retryPendingRelations applies the waiting relations whose records have arrived.
func (a *applier) retryPendingRelations(ctx context.Context) error {
	rows, err := a.tx.Query(ctx, `SELECT source::text, target::text, kind::text, state::text, created_at, updated_at FROM sync_pending_relations`)
	if err != nil {
		return err
	}
	pending, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (relationLine, error) {
		l := relationLine{Type: typeRelation}
		err := r.Scan(&l.Source, &l.Target, &l.Kind, &l.State, &l.CreatedAt, &l.UpdatedAt)
		return l, err
	})
	if err != nil {
		return err
	}
	for _, l := range pending {
		if _, err := a.tx.Exec(ctx, `DELETE FROM sync_pending_relations WHERE source = $1::uuid AND target = $2::uuid AND kind = $3::text::relation_kind`,
			l.Source, l.Target, l.Kind); err != nil {
			return err
		}
		// applyRelation stores it again if a record is still missing.
		if err := a.applyRelation(ctx, l); err != nil {
			return err
		}
	}
	return nil
}

// applyDeletion deletes the record everywhere; the tombstone keeps later copies from
// recreating it.
func (a *applier) applyDeletion(ctx context.Context, in deletionLine) error {
	syncID, err := a.resolve(ctx, in.SyncID)
	if err != nil {
		return err
	}
	if _, err := a.tx.Exec(ctx, `DELETE FROM memories WHERE sync_id = ANY($1::text[]::uuid[])`, []string{in.SyncID, syncID}); err != nil {
		return fmt.Errorf("no se pudo borrar el registro %s: %w", in.SyncID, err)
	}
	if _, err := a.tx.Exec(ctx, `INSERT INTO sync_tombstones (sync_id, deleted_at) SELECT unnest($1::text[]::uuid[]), $2
		ON CONFLICT (sync_id) DO NOTHING`, []string{in.SyncID, syncID}, in.DeletedAt); err != nil {
		return fmt.Errorf("no se pudo registrar el borrado de %s: %w", in.SyncID, err)
	}
	if _, err := a.tx.Exec(ctx, `DELETE FROM sync_pending_relations WHERE source = ANY($1::text[]::uuid[]) OR target = ANY($1::text[]::uuid[])`,
		[]string{in.SyncID, syncID}); err != nil {
		return fmt.Errorf("no se pudieron limpiar las relaciones pendientes de %s: %w", in.SyncID, err)
	}
	return nil
}

// nullInt scans a nullable bigint into dst, leaving 0 for NULL.
func nullInt(dst *int64) any {
	return &nullableInt{dst: dst}
}

type nullableInt struct{ dst *int64 }

func (n *nullableInt) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*n.dst = 0
	case int64:
		*n.dst = v
	default:
		return fmt.Errorf("tipo inesperado %T", src)
	}
	return nil
}
