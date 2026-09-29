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

// maintainer runs the periodic housekeeping. It is used by a single goroutine.
type maintainer struct {
	d        Deps
	trackers *trackerMap
	log      *log.Logger
	now      func() time.Time

	lastUnavailable time.Time
}

// RunMaintenance runs, every interval and until ctx is cancelled: filling the
// pending embeddings of records and topics, closing idle sessions and dropping
// the trackers of idle MCP connections. Errors go to stderr without stopping
// the loop; "Ollama unavailable" is logged at most once every 10 minutes.
func (s *Server) RunMaintenance(ctx context.Context, interval time.Duration) {
	m := &maintainer{d: s.d, trackers: s.trackers, log: log.New(os.Stderr, "lodan: ", log.LstdFlags), now: time.Now}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runOnce(ctx)
		}
	}
}

// runOnce executes one maintenance pass.
func (m *maintainer) runOnce(ctx context.Context) {
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

		if idle := time.Duration(m.d.Cfg.SessionIdleMinutes) * time.Minute; idle > 0 {
			m.trackers.prune(idle)
		}
	}
}

// report logs err, unless the context is done or it is an "Ollama unavailable"
// error already logged in the last 10 minutes.
func (m *maintainer) report(ctx context.Context, what string, err error) {
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
