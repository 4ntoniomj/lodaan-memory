package mcptools

import (
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/session"
)

// unknownClient is the client name used when the MCP client did not identify itself.
const unknownClient = "desconocido"

// trackerEntry is the session tracker of one MCP connection and when it was last used.
type trackerEntry struct {
	tr       *session.Tracker
	lastUsed time.Time
}

// trackerMap binds one session.Tracker to each MCP ServerSession. It is safe
// for concurrent use.
type trackerMap struct {
	mgr *session.Manager
	now func() time.Time

	mu sync.Mutex
	m  map[*mcp.ServerSession]*trackerEntry
}

func newTrackerMap(mgr *session.Manager) *trackerMap {
	return &trackerMap{mgr: mgr, now: time.Now, m: make(map[*mcp.ServerSession]*trackerEntry)}
}

// forSession returns the Tracker of ss, creating it on first use. client is
// called only on creation.
func (t *trackerMap) forSession(ss *mcp.ServerSession, client func() string) *session.Tracker {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[ss]
	if !ok {
		e = &trackerEntry{tr: t.mgr.NewTracker(client())}
		t.m[ss] = e
	}
	e.lastUsed = t.now()
	return e.tr
}

// prune forgets the trackers unused for longer than idle and returns how many
// it removed. Their sessions are closed by session.Manager.CloseIdle.
func (t *trackerMap) prune(idle time.Duration) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := t.now().Add(-idle)
	n := 0
	for ss, e := range t.m {
		if e.lastUsed.Before(cutoff) {
			delete(t.m, ss)
			n++
		}
	}
	return n
}

// size returns the number of trackers held.
func (t *trackerMap) size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}
