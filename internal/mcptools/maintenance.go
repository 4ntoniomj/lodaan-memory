package mcptools

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"lodan/internal/embedding"
)

const (
	// pendingBatch is the number of pending records and topics filled per pass.
	pendingBatch = 64
	// unavailableLogEvery is the minimum time between two "Ollama unavailable" log lines.
	unavailableLogEvery = 10 * time.Minute
)

// Maintenance runs the periodic housekeeping that does not depend on MCP
// connections: filling the pending embeddings of records and topics and closing
// idle sessions. It only uses Deps.Memory, Deps.Topics and Deps.Sessions (any of
// them may be nil), so the supervisor service can run it without an MCP server.
// It is used by a single goroutine.
type Maintenance struct {
	d   Deps
	log *log.Logger
	now func() time.Time

	// afterPass, if set, runs at the end of every pass (the MCP server uses it to
	// drop the trackers of idle connections).
	afterPass func()

	lastUnavailable time.Time
}

// NewMaintenance creates the housekeeping over d. Messages go to logger.
func NewMaintenance(d Deps, logger *log.Logger) *Maintenance {
	return &Maintenance{d: d, log: logger, now: time.Now}
}

// Run calls RunOnce every interval until ctx is cancelled. Errors are logged
// without stopping the loop; "Ollama unavailable" is logged at most once every
// 10 minutes.
func (m *Maintenance) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.RunOnce(ctx)
		}
	}
}

// RunOnce executes one maintenance pass.
func (m *Maintenance) RunOnce(ctx context.Context) {
	if m.d.Memory != nil {
		n, err := m.d.Memory.FillPending(ctx, pendingBatch)
		if n > 0 {
			m.log.Printf("mantenimiento: %d embeddings de registros completados", n)
		}
		m.report(ctx, "embeddings pendientes de registros", err)
	}
	if m.d.Topics != nil {
		n, err := m.d.Topics.FillPending(ctx, pendingBatch)
		if n > 0 {
			m.log.Printf("mantenimiento: %d embeddings de temas completados", n)
		}
		m.report(ctx, "embeddings pendientes de temas", err)
	}
	if m.d.Sessions != nil {
		_, err := m.d.Sessions.CloseIdle(ctx)
		m.report(ctx, "cierre de sesiones inactivas", err)
	}
	if m.afterPass != nil {
		m.afterPass()
	}
}

// RunMaintenance runs, every interval and until ctx is cancelled: filling the
// pending embeddings of records and topics, closing idle sessions and dropping
// the trackers of idle MCP connections. Errors go to stderr without stopping
// the loop; "Ollama unavailable" is logged at most once every 10 minutes.
func (s *Server) RunMaintenance(ctx context.Context, interval time.Duration) {
	m := NewMaintenance(s.d, log.New(os.Stderr, "lodan: ", log.LstdFlags))
	m.afterPass = func() {
		if s.d.Sessions == nil {
			return
		}
		if idle := time.Duration(s.d.Cfg.SessionIdleMinutes) * time.Minute; idle > 0 {
			s.trackers.prune(idle)
		}
	}
	m.Run(ctx, interval)
}

// report logs err, unless the context is done or it is an "Ollama unavailable"
// error already logged in the last 10 minutes.
func (m *Maintenance) report(ctx context.Context, what string, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	if errors.Is(err, embedding.ErrUnavailable) {
		now := m.now()
		if !m.lastUnavailable.IsZero() && now.Sub(m.lastUnavailable) < unavailableLogEvery {
			return
		}
		m.lastUnavailable = now
	}
	m.log.Printf("mantenimiento: %s: %v", what, err)
}
