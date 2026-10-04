// Package session manages the lifecycle of conversation sessions: lazy creation, closing by
// inactivity, end-of-session summaries and lookup ("last conversation") with filters.
package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/database"
)

const (
	// maxClientLen is the maximum number of characters stored in sessions.client.
	maxClientLen = 100
	// maxSummaryLen is the maximum number of characters stored in sessions.summary.
	maxSummaryLen = 2000
	// unknownClient is stored when the MCP client did not identify itself.
	unknownClient = "desconocido"

	// defaultListLimit and maxListLimit bound the result of List.
	defaultListLimit = 5
	maxListLimit     = 20
)

// Session is a conversation session as stored in the database.
type Session struct {
	ID             string
	Client         string
	StartedAt      time.Time
	LastActivityAt time.Time
	// EndedAt is nil while the session is open.
	EndedAt *time.Time
	Summary string
	// Topics holds the slugs of the topics linked to the session, sorted alphabetically.
	Topics []string
}

// Filter narrows down Last and List. Zero-valued fields are ignored.
type Filter struct {
	// TopicSlug matches sessions linked to the topic with exactly this slug (the caller
	// normalizes it beforehand).
	TopicSlug string
	// Client matches sessions whose client contains this text, ignoring case.
	Client string
	// Date matches sessions started on the calendar day of *Date, in the time zone of *Date.
	Date *time.Time
	// ExcludeID leaves out the session with this id.
	ExcludeID string
}

// Manager creates trackers and queries sessions. It is safe for concurrent use.
type Manager struct {
	pool *pgxpool.Pool
	idle time.Duration
	// now is the clock; tests replace it.
	now func() time.Time
}

// NewManager returns a Manager that closes sessions after idle without activity.
func NewManager(pool *pgxpool.Pool, idle time.Duration) *Manager {
	return &Manager{pool: pool, idle: idle, now: time.Now}
}

// AddTopics links topics to a session; links that already exist are ignored. It runs on q so
// it can take part in the caller's transaction.
func (m *Manager) AddTopics(ctx context.Context, q database.Querier, sessionID string, topicIDs []int32) error {
	if len(topicIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO session_topics (session_id, topic_id)
		SELECT $1::text, unnest($2::int[])
		ON CONFLICT DO NOTHING`, sessionID, topicIDs)
	if err != nil {
		return fmt.Errorf("no se pudieron asociar los temas a la sesión %s: %w", sessionID, err)
	}
	return nil
}

// Last returns the most recently started session that matches f, or nil, nil if there is none.
func (m *Manager) Last(ctx context.Context, f Filter) (*Session, error) {
	list, err := m.query(ctx, f, 1)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// List returns the sessions that match f, most recently started first. limit defaults to 5
// when it is not positive and is capped at 20.
func (m *Manager) List(ctx context.Context, f Filter, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return m.query(ctx, f, limit)
}

// CloseIdle closes every open session whose last activity is older than the idle time, using
// that last activity as the end time. It returns the number of sessions closed.
func (m *Manager) CloseIdle(ctx context.Context) (int64, error) {
	cutoff := m.now().Add(-m.idle)
	tag, err := m.pool.Exec(ctx, `UPDATE sessions SET ended_at = last_activity_at
		WHERE ended_at IS NULL AND last_activity_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("no se pudieron cerrar las sesiones inactivas: %w", err)
	}
	return tag.RowsAffected(), nil
}

// query runs the filtered lookup shared by Last and List.
func (m *Manager) query(ctx context.Context, f Filter, limit int) ([]Session, error) {
	var (
		conds []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if f.TopicSlug != "" {
		conds = append(conds, `EXISTS (SELECT 1 FROM session_topics ft JOIN topics t ON t.id = ft.topic_id
			WHERE ft.session_id = s.id AND t.slug = `+arg(f.TopicSlug)+`)`)
	}
	if f.Client != "" {
		conds = append(conds, "s.client ILIKE "+arg("%"+escapeLike(f.Client)+"%"))
	}
	if f.Date != nil {
		start := time.Date(f.Date.Year(), f.Date.Month(), f.Date.Day(), 0, 0, 0, 0, f.Date.Location())
		end := start.AddDate(0, 0, 1)
		conds = append(conds, "s.started_at >= "+arg(start)+" AND s.started_at < "+arg(end))
	}
	if f.ExcludeID != "" {
		conds = append(conds, "s.id <> "+arg(f.ExcludeID))
	}

	sql := `SELECT s.id, s.client, s.started_at, s.last_activity_at, s.ended_at, COALESCE(s.summary, ''),
		COALESCE((SELECT array_agg(t.slug ORDER BY t.slug)
			FROM session_topics st JOIN topics t ON t.id = st.topic_id
			WHERE st.session_id = s.id), ARRAY[]::text[])
		FROM sessions s`
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	sql += " ORDER BY s.started_at DESC, s.id DESC LIMIT " + arg(limit)

	rows, err := m.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron consultar las sesiones: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Client, &s.StartedAt, &s.LastActivityAt, &s.EndedAt, &s.Summary, &s.Topics); err != nil {
			return nil, fmt.Errorf("no se pudo leer una sesión: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error al recorrer las sesiones: %w", err)
	}
	return out, nil
}

// escapeLike escapes the LIKE wildcards in s so it is matched literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// truncate cuts s to at most n characters (runes), never splitting a multi-byte character.
func truncate(s string, n int) string {
	if len(s) <= n { // a string of at most n bytes has at most n runes
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Format renders a session as one compact line in local time, for example:
//
//	2026-09-29 14:32 · claude-code · temas: lodan, entrenamiento · abierta · resumen: texto
//
// The topics part is omitted when the session has no topics.
func Format(s Session) string {
	parts := []string{s.StartedAt.Local().Format("2006-01-02 15:04"), s.Client}
	if len(s.Topics) > 0 {
		parts = append(parts, "temas: "+strings.Join(s.Topics, ", "))
	}
	if s.EndedAt == nil {
		parts = append(parts, "abierta")
	} else {
		parts = append(parts, "cerrada")
	}
	if summary := strings.Join(strings.Fields(s.Summary), " "); summary != "" {
		parts = append(parts, "resumen: "+summary)
	} else {
		parts = append(parts, "sin resumen")
	}
	return strings.Join(parts, " · ")
}
