package session

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/oklog/ulid/v2"
)

// Tracker follows the active session of one MCP connection. The session is created lazily on
// the first Current call. It is safe for concurrent use.
type Tracker struct {
	m      *Manager
	client string

	mu sync.Mutex
	id string // empty when the connection has no active session
}

// NewTracker returns a Tracker for a new MCP connection of the given client. The client name
// is trimmed to 100 characters; an empty one is stored as "desconocido".
func (m *Manager) NewTracker(client string) *Tracker {
	client = truncate(strings.TrimSpace(client), maxClientLen)
	if client == "" {
		client = unknownClient
	}
	return &Tracker{m: m, client: client}
}

// ID returns the id of the current session, or "" if the connection has none. It never
// touches the database.
func (t *Tracker) ID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.id
}

// Current returns the id of the active session and records activity on it. It creates a new
// session if there is none, if the current one was idle for longer than the idle time (it is
// closed at its last activity) or if another instance already closed it.
func (t *Tracker) Current(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.m.now()
	if t.id != "" {
		alive, err := t.refresh(ctx)
		if err != nil {
			return "", err
		}
		if alive {
			return t.id, nil
		}
	}

	id := ulid.Make().String()
	_, err := t.m.pool.Exec(ctx, `INSERT INTO sessions (id, client, started_at, last_activity_at)
		VALUES ($1, $2, $3, $3)`, id, t.client, now)
	if err != nil {
		return "", fmt.Errorf("no se pudo crear la sesión: %w", err)
	}
	t.id = id
	return id, nil
}

// Touch records activity on the current session. It never creates one; if the session has
// expired it is closed and forgotten, so the next Current call opens a new one.
func (t *Tracker) Touch(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.id == "" {
		return nil
	}
	_, err := t.refresh(ctx)
	return err
}

// End stores the summary (trimmed to 2000 characters), closes the session and leaves the
// Tracker without one. If there was no session it creates one already closed with that
// summary. It returns the id of the closed session.
func (t *Tracker) End(ctx context.Context, summary string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	summary = truncate(strings.TrimSpace(summary), maxSummaryLen)
	now := t.m.now()

	if t.id != "" {
		// Keep an end time set by someone else (another instance or CloseIdle) and an
		// existing summary if the new one is empty.
		tag, err := t.m.pool.Exec(ctx, `UPDATE sessions
			SET summary = COALESCE(NULLIF($2::text, ''), summary), ended_at = COALESCE(ended_at, $3)
			WHERE id = $1`, t.id, summary, now)
		if err != nil {
			return "", fmt.Errorf("no se pudo cerrar la sesión %s: %w", t.id, err)
		}
		if tag.RowsAffected() > 0 {
			id := t.id
			t.id = ""
			return id, nil
		}
		// The row no longer exists: fall through and record the summary in a new session.
		t.id = ""
	}

	id := ulid.Make().String()
	_, err := t.m.pool.Exec(ctx, `INSERT INTO sessions (id, client, started_at, last_activity_at, ended_at, summary)
		VALUES ($1, $2, $3, $3, $3, NULLIF($4::text, ''))`, id, t.client, now, summary)
	if err != nil {
		return "", fmt.Errorf("no se pudo guardar la sesión con su resumen: %w", err)
	}
	return id, nil
}

// refresh records activity now on the current session if it is still valid (open and not idle
// for longer than the idle time). Otherwise it closes it at its last activity, forgets it and
// returns false. t.mu must be held and t.id must not be empty.
func (t *Tracker) refresh(ctx context.Context) (bool, error) {
	now := t.m.now()
	tag, err := t.m.pool.Exec(ctx, `UPDATE sessions SET last_activity_at = $2
		WHERE id = $1 AND ended_at IS NULL AND last_activity_at >= $3`,
		t.id, now, now.Add(-t.m.idle))
	if err != nil {
		return false, fmt.Errorf("no se pudo actualizar la actividad de la sesión %s: %w", t.id, err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}

	// Expired or already closed elsewhere; closing is a no-op in the second case.
	_, err = t.m.pool.Exec(ctx, `UPDATE sessions SET ended_at = last_activity_at
		WHERE id = $1 AND ended_at IS NULL`, t.id)
	if err != nil {
		return false, fmt.Errorf("no se pudo cerrar la sesión caducada %s: %w", t.id, err)
	}
	t.id = ""
	return false, nil
}
