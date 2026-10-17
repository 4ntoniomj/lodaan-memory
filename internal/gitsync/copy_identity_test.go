package gitsync_test

import (
	"context"
	"testing"
	"time"

	"lodan/internal/memory"
)

// The superseded copy created by a concurrent edit must have the same identity on both
// machines, so a later change to it (deleting it) reaches the other machine.
func TestMergeC1_LaCopiaTieneLaMismaIdentidadYElBorradoLlega(t *testing.T) {
	p := newPair(t)
	idA, idB, sync := seed(t, p, "Dato", "zulu contenido inicial")

	p.a.revise(t, memory.Revision{ID: idA, Action: "update", Content: "zulu version de a"})
	time.Sleep(pause)
	p.b.revise(t, memory.Revision{ID: idB, Action: "update", Content: "zulu version de b"})
	p.exchange(t)
	p.exchange(t)

	copySync := func(m *machine) string {
		var s string
		if err := m.pool.QueryRow(context.Background(),
			`SELECT sync_id::text FROM memories WHERE content=$1 AND status='superseded'`, "zulu version de a").Scan(&s); err != nil {
			t.Fatalf("copia superseded en %s: %v", m.id, err)
		}
		return s
	}
	sa, sb := copySync(p.a), copySync(p.b)
	if sa != sb {
		t.Fatalf("la copia tiene sync_id distinto: A=%s B=%s", sa, sb)
	}

	copyA := p.a.idBySync(t, sa)
	p.a.revise(t, memory.Revision{ID: copyA, Action: "delete", Confirm: true})
	p.exchange(t)
	p.exchange(t)

	for _, m := range []*machine{p.a, p.b} {
		if n := m.count(t, "content = $1", "zulu version de a"); n != 0 {
			t.Errorf("%s conserva la copia borrada (%d)", m.id, n)
		}
		win := m.idBySync(t, sync)
		if got := m.full(t, win); got.Content != "zulu version de b" || got.Status != memory.StatusActive {
			t.Errorf("%s: el ganador es %q/%s, quería activo con la versión de b", m.id, got.Content, got.Status)
		}
	}
	requireConverged(t, p)
}
