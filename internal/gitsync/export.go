package gitsync

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// State keys in sync_state.
const (
	stateExportSnapshot = "export_snapshot" // pg_snapshot of the previous export
	stateExportSeq      = "export_seq"      // counter of the files written
)

// changedSQL selects rows whose last local change was committed after the previous export
// and before the current one. A row's sync_xid is the transaction of its last local change;
// imported rows have none. Comparing against the two snapshots exports each change exactly
// once, even when transactions commit out of order.
const changedSQL = `%[1]s.sync_xid IS NOT NULL
	AND pg_visible_in_snapshot(%[1]s.sync_xid, $1::text::pg_snapshot)
	AND ($2::text IS NULL OR NOT pg_visible_in_snapshot(%[1]s.sync_xid, $2::text::pg_snapshot))`

// Export writes every local change since the previous export into one encrypted batch file
// under opts.Dir/changes/<MachineID>/. It writes nothing when there are no changes.
func Export(ctx context.Context, pool *pgxpool.Pool, opts ExportOptions) (ExportResult, error) {
	if err := checkMachineID(opts.MachineID); err != nil {
		return ExportResult{}, err
	}
	recipient, err := age.ParseX25519Recipient(opts.Recipient)
	if err != nil {
		return ExportResult{}, fmt.Errorf("destinatario de cifrado no válido: %w", err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ExportResult{}, fmt.Errorf("no se pudo iniciar la exportación: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The first statement fixes the transaction snapshot; pg_current_snapshot returns it.
	var cur string
	if err := tx.QueryRow(ctx, `SELECT pg_current_snapshot()::text`).Scan(&cur); err != nil {
		return ExportResult{}, fmt.Errorf("no se pudo leer la instantánea: %w", err)
	}
	prev, err := readState(ctx, tx, stateExportSnapshot)
	if err != nil {
		return ExportResult{}, err
	}
	var prevArg any
	if prev != "" {
		prevArg = prev
	}

	sessions, err := exportSessions(ctx, tx, cur, prevArg)
	if err != nil {
		return ExportResult{}, err
	}
	memories, err := exportMemories(ctx, tx, cur, prevArg)
	if err != nil {
		return ExportResult{}, err
	}
	relations, err := exportRelations(ctx, tx, cur, prevArg)
	if err != nil {
		return ExportResult{}, err
	}
	deletions, err := exportDeletions(ctx, tx, cur, prevArg)
	if err != nil {
		return ExportResult{}, err
	}
	aliases, err := exportAliases(ctx, tx, cur, prevArg)
	if err != nil {
		return ExportResult{}, err
	}

	res := ExportResult{Memories: len(memories), Relations: len(relations), Sessions: len(sessions), Deletions: len(deletions)}
	if err := writeState(ctx, tx, stateExportSnapshot, cur); err != nil {
		return ExportResult{}, err
	}
	if res.Memories+res.Relations+res.Sessions+res.Deletions+len(aliases) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return ExportResult{}, fmt.Errorf("no se pudo guardar el estado de la exportación: %w", err)
		}
		return ExportResult{}, nil
	}

	seqText, err := readState(ctx, tx, stateExportSeq)
	if err != nil {
		return ExportResult{}, err
	}
	seq, _ := strconv.ParseInt(seqText, 10, 64)
	seq++
	if err := writeState(ctx, tx, stateExportSeq, strconv.FormatInt(seq, 10)); err != nil {
		return ExportResult{}, err
	}

	// The exported versions become the last versions exchanged. lodan.sync_import keeps these
	// bookkeeping updates from counting as local changes.
	if _, err := tx.Exec(ctx, `SET LOCAL lodan.sync_import = 'on'`); err != nil {
		return ExportResult{}, fmt.Errorf("no se pudo preparar la exportación: %w", err)
	}
	ids := make([]string, len(memories))
	for i, m := range memories {
		ids[i] = m.SyncID
	}
	if _, err := tx.Exec(ctx, `UPDATE memories SET sync_base = sync_rev, sync_dirty = false
		WHERE sync_id = ANY($1::text[]::uuid[])`, ids); err != nil {
		return ExportResult{}, fmt.Errorf("no se pudo marcar lo exportado: %w", err)
	}

	now := time.Now().UTC()
	rel := "changes/" + opts.MachineID + "/" + fileName(now, seq)
	path := filepath.Join(opts.Dir, filepath.FromSlash(rel))
	// The batch file uses FormatV2 only when it carries alias lines; otherwise
	// it stays in Format so machines that have not picked up typeAlias still read it.
	format := Format
	if len(aliases) > 0 {
		format = FormatV2
	}
	hdr := headerLine{Type: typeHeader, Format: format, Machine: opts.MachineID, CreatedAt: now}
	if err := writeBatch(path, recipient, hdr, sessions, memories, relations, deletions, aliases); err != nil {
		return ExportResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = os.Remove(path)
		return ExportResult{}, fmt.Errorf("no se pudo guardar el estado de la exportación: %w", err)
	}
	res.File = rel
	return res, nil
}

func exportSessions(ctx context.Context, tx pgx.Tx, cur string, prev any) ([]sessionLine, error) {
	rows, err := tx.Query(ctx, `SELECT s.id, s.client, s.started_at, s.last_activity_at, s.ended_at, coalesce(s.summary, ''),
			coalesce((SELECT array_agg(t.slug ORDER BY t.slug) FROM session_topics st JOIN topics t ON t.id = st.topic_id
			          WHERE st.session_id = s.id), '{}')
		FROM sessions s WHERE `+fmt.Sprintf(changedSQL, "s")+` ORDER BY s.id`, cur, prev)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer las sesiones: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (sessionLine, error) {
		l := sessionLine{Type: typeSession}
		err := r.Scan(&l.ID, &l.Client, &l.StartedAt, &l.LastActivityAt, &l.EndedAt, &l.Summary, &l.Topics)
		return l, err
	})
}

func exportMemories(ctx context.Context, tx pgx.Tx, cur string, prev any) ([]memoryLine, error) {
	rows, err := tx.Query(ctx, `SELECT m.sync_id::text, m.sync_rev::text, coalesce(m.sync_base::text, ''), m.kind::text, m.status::text,
			m.title, m.content, coalesce(m.key, ''), coalesce(m.session_id, ''), m.occurred_at, m.created_at, m.updated_at,
			coalesce((SELECT array_agg(t.slug ORDER BY t.slug) FROM memory_topics mt JOIN topics t ON t.id = mt.topic_id
			          WHERE mt.memory_id = m.id), '{}')
		FROM memories m WHERE `+fmt.Sprintf(changedSQL, "m")+` ORDER BY m.id`, cur, prev)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los registros: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (memoryLine, error) {
		l := memoryLine{Type: typeMemory}
		err := r.Scan(&l.SyncID, &l.Rev, &l.Base, &l.Kind, &l.Status, &l.Title, &l.Content, &l.Key, &l.SessionID,
			&l.OccurredAt, &l.CreatedAt, &l.UpdatedAt, &l.Topics)
		return l, err
	})
}

func exportRelations(ctx context.Context, tx pgx.Tx, cur string, prev any) ([]relationLine, error) {
	rows, err := tx.Query(ctx, `SELECT s.sync_id::text, g.sync_id::text, r.kind::text, r.state::text,
			COALESCE(r.verdict, ''), COALESCE(r.confidence, 0)::float8, r.created_at, r.updated_at
		FROM memory_relations r JOIN memories s ON s.id = r.source_id JOIN memories g ON g.id = r.target_id
		WHERE `+fmt.Sprintf(changedSQL, "r")+` ORDER BY r.created_at, s.id, g.id`, cur, prev)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer las relaciones: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (relationLine, error) {
		l := relationLine{Type: typeRelation}
		err := r.Scan(&l.Source, &l.Target, &l.Kind, &l.State, &l.Verdict, &l.Confidence, &l.CreatedAt, &l.UpdatedAt)
		return l, err
	})
}

func exportDeletions(ctx context.Context, tx pgx.Tx, cur string, prev any) ([]deletionLine, error) {
	rows, err := tx.Query(ctx, `SELECT d.sync_id::text, d.deleted_at FROM sync_tombstones d
		WHERE `+fmt.Sprintf(changedSQL, "d")+` ORDER BY d.deleted_at`, cur, prev)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los borrados: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (deletionLine, error) {
		l := deletionLine{Type: typeDeletion}
		err := r.Scan(&l.SyncID, &l.DeletedAt)
		return l, err
	})
}

// exportAliases reads every confirmed and rejected tag alias whose last local
// change is between prev and cur. Suggested aliases stay local: the trigger
// sets their sync_xid to NULL, so changedSQL skips them.
func exportAliases(ctx context.Context, tx pgx.Tx, cur string, prev any) ([]aliasLine, error) {
	rows, err := tx.Query(ctx, `SELECT alias, canonical, state::text, updated_at
		FROM tag_aliases
		WHERE state <> 'suggested' AND `+fmt.Sprintf(changedSQL, "tag_aliases")+`
		ORDER BY alias, canonical`, cur, prev)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los alias: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (aliasLine, error) {
		l := aliasLine{Type: typeAlias}
		err := r.Scan(&l.Alias, &l.Canonical, &l.State, &l.UpdatedAt)
		return l, err
	})
}

// writeBatch encrypts the batch to path, through a temporary file renamed at the end.
func writeBatch(path string, recipient age.Recipient, hdr headerLine, sessions []sessionLine, memories []memoryLine,
	relations []relationLine, deletions []deletionLine, aliases []aliasLine) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("no se pudo crear la carpeta de cambios: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("no se pudo crear el fichero de cambios: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	enc, err := age.Encrypt(tmp, recipient)
	if err != nil {
		return fmt.Errorf("no se pudo cifrar el fichero de cambios: %w", err)
	}
	w := bufio.NewWriter(enc)
	je := json.NewEncoder(w)
	je.SetEscapeHTML(false)
	put := func(v any) {
		if err == nil {
			err = je.Encode(v)
		}
	}
	put(hdr)
	for _, l := range sessions {
		put(l)
	}
	for _, l := range memories {
		put(l)
	}
	for _, l := range relations {
		put(l)
	}
	for _, l := range aliases {
		put(l)
	}
	for _, l := range deletions {
		put(l)
	}
	if err != nil {
		return fmt.Errorf("no se pudo escribir el fichero de cambios: %w", err)
	}
	if err = w.Flush(); err != nil {
		return fmt.Errorf("no se pudo escribir el fichero de cambios: %w", err)
	}
	if err = enc.Close(); err != nil {
		return fmt.Errorf("no se pudo cerrar el cifrado: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("no se pudo cerrar el fichero de cambios: %w", err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("no se pudo guardar el fichero de cambios: %w", err)
	}
	return nil
}

func readState(ctx context.Context, q pgx.Tx, key string) (string, error) {
	var v string
	err := q.QueryRow(ctx, `SELECT value FROM sync_state WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("no se pudo leer el estado de sincronización %s: %w", key, err)
	}
	return v, nil
}

func writeState(ctx context.Context, q pgx.Tx, key, value string) error {
	if _, err := q.Exec(ctx, `INSERT INTO sync_state (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
		return fmt.Errorf("no se pudo guardar el estado de sincronización %s: %w", key, err)
	}
	return nil
}
