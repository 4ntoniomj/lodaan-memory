package gitsync_test

// Verifier probes (independent of the sealed tests).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/config"
	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/gitsync"
	"lodan/internal/memory"
	"lodan/internal/session"
	"lodan/internal/topic"
)

// origTables lists the pre-0003 columns of every data table, so the same query works
// before and after the migration.
var origTables = map[string]string{
	"memories": `SELECT id, kind, status, title, content, coalesce(key,'~'), encode(content_hash,'hex'), coalesce(session_id,'~'),
		occurred_at, created_at, updated_at, coalesce(embedding::text,'~'), coalesce(embedding_model::text,'~') FROM memories ORDER BY id`,
	"memory_relations": `SELECT source_id, target_id, kind, state, created_at FROM memory_relations ORDER BY 1,2,3`,
	"sessions":         `SELECT id, client, started_at, last_activity_at, coalesce(ended_at::text,'~'), coalesce(summary,'~') FROM sessions ORDER BY id`,
	"topics":           `SELECT id, slug, coalesce(embedding::text,'~'), created_at, last_used_at FROM topics ORDER BY id`,
	"memory_topics":    `SELECT topic_id, memory_id, kind, active, ts FROM memory_topics ORDER BY 1,2`,
	"session_topics":   `SELECT session_id, topic_id FROM session_topics ORDER BY 1,2`,
}

func tableHash(t *testing.T, pool *pgxpool.Pool, q string) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	h := sha256.New()
	n := 0
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%v\n", vals)
		n++
	}
	return fmt.Sprintf("%d:%s", n, hex.EncodeToString(h.Sum(nil))[:16])
}

func origHashes(t *testing.T, pool *pgxpool.Pool) map[string]string {
	out := map[string]string{}
	for name, q := range origTables {
		out[name] = tableHash(t, pool, q)
	}
	return out
}

// allHashes also covers the sync bookkeeping tables (post-0003 only).
func allHashes(t *testing.T, pool *pgxpool.Pool) map[string]string {
	out := origHashes(t, pool)
	extra := map[string]string{
		"memories_sync":     `SELECT id, sync_id, sync_rev, coalesce(sync_base::text,'~'), sync_dirty, coalesce(sync_xid::text,'~') FROM memories ORDER BY id`,
		"sync_revs":         `SELECT sync_id, rev FROM sync_revs ORDER BY 1,2`,
		"sync_tombstones":   `SELECT sync_id, deleted_at FROM sync_tombstones ORDER BY 1`,
		"sync_aliases":      `SELECT alias, sync_id FROM sync_aliases ORDER BY 1`,
		"sync_pending":      `SELECT source, target, kind, state FROM sync_pending_relations ORDER BY 1,2,3`,
		"sync_imported":     `SELECT path FROM sync_imported_files ORDER BY 1`,
		"sync_state":        `SELECT key, value FROM sync_state ORDER BY 1`,
		"relations_updated": `SELECT source_id, target_id, kind, updated_at FROM memory_relations ORDER BY 1,2,3`,
	}
	for name, q := range extra {
		out[name] = tableHash(t, pool, q)
	}
	return out
}

// PROBE S1/S2/S3: migration 0003 over data created with the pre-0003 schema.
func TestProbeMigracion0003SobreDatosExistentes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tc := database.NewTestCluster(t, testDims)
	if _, err := tc.Pool.Exec(ctx, `CREATE DATABASE pre`); err != nil {
		t.Fatal(err)
	}
	dsn, err := tc.Cluster.DSN("pre")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// Baseline schema: 0001 + 0002 exactly as the baseline binary applies them.
	for _, f := range []string{"0001_init.sql", "0002_topic_profile_indexes.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "database", "migrations", f))
		if err != nil {
			t.Fatal(err)
		}
		sql := strings.ReplaceAll(string(raw), "{{.Dims}}", fmt.Sprint(testDims))
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
		INSERT INTO schema_migrations (version) VALUES (1),(2);
		INSERT INTO lodan_settings VALUES ('embed_dims','64'),('embed_model','test-model')`); err != nil {
		t.Fatal(err)
	}

	// Seed realistic data with the (unchanged) memory service.
	cfg := config.Default()
	emb := &embedding.FakeEmbedder{Dimensions: testDims}
	mgr := session.NewManager(pool, 30*time.Minute)
	svc := memory.NewService(pool, emb, topic.NewResolver(pool, emb, cfg.TopicSimilarity), mgr, cfg.DupSimilarity, 30*time.Second)
	tr := mgr.NewTracker("claude-code")
	saved, err := svc.Remember(ctx, tr, []memory.Item{
		{Title: "Matrícula", Content: "la matrícula es 1111 AAA", Kind: memory.KindFact, Key: "coche-matricula", Topics: []string{"coche"}},
		{Title: "Entreno", Content: "entreno por la tarde los martes", Kind: memory.KindPreference, Topics: []string{"deporte", "rutina"}},
		{Title: "Postgres", Content: "decidimos usar Postgres 16 para lodan", Kind: memory.KindDecision, Topics: []string{"lodan"}},
		{Title: "Churros", Content: "desayuné churros el 2026-10-10", Kind: memory.KindEvent, Topics: []string{"comida"}},
		{Title: "Borrar", Content: "registro que se borrará antes de migrar", Kind: memory.KindNote},
		{Title: "Invalidar", Content: "dato erróneo que se invalida", Kind: memory.KindFact, Topics: []string{"coche"}},
	})
	if err != nil || len(saved) != 6 {
		t.Fatalf("Remember: %v %d", err, len(saved))
	}
	if _, err := svc.Remember(ctx, tr, []memory.Item{{Title: "Matrícula", Content: "la matrícula es 2222 BBB", Kind: memory.KindFact, Key: "coche-matricula", Topics: []string{"coche"},
		Relate: []memory.RelateTo{{ID: saved[1].ID, Kind: "related"}}}}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []memory.Revision{
		{ID: saved[5].ID, Action: "invalidate"},
		{ID: saved[2].ID, Action: "relate", WithID: saved[3].ID, Relation: "part_of"},
		{ID: saved[1].ID, Action: "update", Content: "entreno por la tarde los martes y jueves"},
		{ID: saved[4].ID, Action: "delete", Confirm: true},
	} {
		if _, err := svc.Revise(ctx, tr, r); err != nil {
			t.Fatalf("Revise %s: %v", r.Action, err)
		}
	}
	if _, err := tr.End(ctx, "resumen de la sesión previa a la migración"); err != nil {
		t.Fatal(err)
	}
	tr2 := mgr.NewTracker("opencode")
	if _, err := svc.Remember(ctx, tr2, []memory.Item{{Title: "Otra", Content: "nota abierta en otra sesión", Kind: memory.KindNote, Topics: []string{"lodan"}}}); err != nil {
		t.Fatal(err)
	}

	before := origHashes(t, pool)
	if err := database.Migrate(ctx, pool, database.Settings{Dims: testDims, Model: "test-model"}); err != nil {
		t.Fatalf("Migrate 0003: %v", err)
	}
	after := origHashes(t, pool)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("migration changed existing data:\nbefore %v\nafter  %v", before, after)
	}

	var n, distinct, nulls int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT sync_id), count(*) FILTER (WHERE sync_id IS NULL) FROM memories`).Scan(&n, &distinct, &nulls); err != nil {
		t.Fatal(err)
	}
	if n != distinct || nulls != 0 || n == 0 {
		t.Errorf("sync_id: %d rows, %d distinct, %d null", n, distinct, nulls)
	}
	var nRel, nSess int
	_ = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM memory_relations), (SELECT count(*) FROM sessions)`).Scan(&nRel, &nSess)

	id, _ := age.GenerateX25519Identity()
	dir := t.TempDir()
	exp := func() gitsync.ExportResult {
		res, err := gitsync.Export(ctx, pool, gitsync.ExportOptions{Dir: dir, MachineID: "m1", Recipient: id.Recipient().String()})
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		return res
	}
	r1 := exp()
	t.Logf("first export: %+v (memories %d, relations %d, sessions %d)", r1, n, nRel, nSess)
	if r1.File == "" || r1.Memories != n || r1.Relations != nRel || r1.Sessions != nSess || r1.Deletions != 0 {
		t.Errorf("first export after migration = %+v, want %d memories, %d relations, %d sessions, 0 deletions", r1, n, nRel, nSess)
	}
	if after2 := origHashes(t, pool); !reflect.DeepEqual(before, after2) {
		t.Errorf("first export changed user data:\nbefore %v\nafter  %v", before, after2)
	}
	if r2 := exp(); r2.File != "" {
		t.Errorf("second export wrote %+v, want nothing", r2)
	}

	// Out-of-order commit: a change committed after an export that started while it was open.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE memories SET title = 'Postgres 16', updated_at = now() WHERE id = $1`, saved[2].ID); err != nil {
		t.Fatal(err)
	}
	if r := exp(); r.File != "" {
		t.Errorf("export with an uncommitted change wrote %+v", r)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if r := exp(); r.Memories != 1 || r.Relations+r.Sessions+r.Deletions != 0 {
		t.Errorf("export after the late commit = %+v, want exactly 1 memory", r)
	}
	if r := exp(); r.File != "" {
		t.Errorf("export after that wrote %+v, want nothing", r)
	}
}

// PROBE S5/invariant: rejected imports change nothing.
func TestProbeImportRechazadoNoCambiaNada(t *testing.T) {
	p := newPair(t)
	p.b.rem(t, "Local B", "registro local de la máquina b", memory.KindFact, "k1", "tema-b")
	p.a.rem(t, "Remoto A", "registro remoto de la máquina a", memory.KindFact, "", "tema-a")
	ctx := context.Background()

	other, _ := age.GenerateX25519Identity()
	if _, err := gitsync.Export(ctx, p.a.pool, gitsync.ExportOptions{Dir: p.dir, MachineID: "maquina-a", Recipient: other.Recipient().String()}); err != nil {
		t.Fatal(err)
	}
	before := allHashes(t, p.b.pool)
	res, err := gitsync.Import(ctx, p.b.pool, gitsync.ImportOptions{Dir: p.dir, MachineID: "maquina-b", Identity: p.identity})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Files != 0 || len(res.Errors) != 1 || !strings.HasPrefix(res.Errors[0], "no se pudo descifrar changes/maquina-a/") ||
		!strings.HasSuffix(res.Errors[0], ": la clave no coincide") {
		t.Errorf("Import with another key = %+v", res)
	}
	if after := allHashes(t, p.b.pool); !reflect.DeepEqual(before, after) {
		t.Errorf("rejected import changed the database:\nbefore %v\nafter  %v", before, after)
	}

	// A correctly encrypted file whose second record is invalid: nothing of it may be applied.
	os.RemoveAll(filepath.Join(p.dir, "changes"))
	path := filepath.Join(p.dir, "changes", "maquina-c", "20260101T000000Z-1.jsonl.age")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, _ := os.Create(path)
	w, _ := age.Encrypt(f, p.ageID.Recipient())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	fmt.Fprintf(w, `{"type":"header","format":"lodan-sync/1","machine":"maquina-c","created_at":"%s"}`+"\n", now)
	fmt.Fprintf(w, `{"type":"memory","sync_id":"11111111-1111-4111-8111-111111111111","rev":"21111111-1111-4111-8111-111111111111","kind":"fact","status":"active","title":"ok","content":"registro válido c","occurred_at":"%[1]s","created_at":"%[1]s","updated_at":"%[1]s","topics":["nuevo-tema"]}`+"\n", now)
	fmt.Fprintf(w, `{"type":"memory","sync_id":"31111111-1111-4111-8111-111111111111","rev":"41111111-1111-4111-8111-111111111111","kind":"bogus","status":"active","title":"x","content":"registro inválido","occurred_at":"%[1]s","created_at":"%[1]s","updated_at":"%[1]s"}`+"\n", now)
	w.Close()
	f.Close()
	before = allHashes(t, p.b.pool)
	res, err = gitsync.Import(ctx, p.b.pool, gitsync.ImportOptions{Dir: p.dir, MachineID: "maquina-b", Identity: p.identity})
	t.Logf("poisoned import: res=%+v err=%v", res, err)
	if after := allHashes(t, p.b.pool); !reflect.DeepEqual(before, after) {
		t.Errorf("failed import changed the database:\nbefore %v\nafter  %v", before, after)
	}

	// A poisoned file must not block later valid files of other machines.
	p.a.rem(t, "Nuevo A", "registro nuevo de la máquina a", memory.KindFact, "", "tema-a")
	p.export(t, p.a)
	res, err = gitsync.Import(ctx, p.b.pool, gitsync.ImportOptions{Dir: p.dir, MachineID: "maquina-b", Identity: p.identity})
	if err != nil || p.b.count(t, "content = $1", "registro nuevo de la máquina a") != 1 {
		t.Errorf("a file that cannot be applied must not block the valid files of other machines: res=%+v err=%v", res, err)
	}
	if len(res.Errors) != 1 || !strings.HasPrefix(res.Errors[0], "no se pudo importar changes/maquina-c/") {
		t.Errorf("the poisoned file must be reported in Errors: %q", res.Errors)
	}
}

func gitAll(t *testing.T, bare string) string {
	return gitOut(t, "--git-dir", bare, "log", "-p", "--all")
}

func push(t *testing.T, m *machine, c gitsync.Config) gitsync.ExportResult {
	t.Helper()
	r, err := gitsync.Push(gctx(t), m.pool, c)
	if err != nil {
		t.Fatalf("Push %s: %v", m.id, err)
	}
	return r
}

func pull(t *testing.T, m *machine, c gitsync.Config) gitsync.ImportResult {
	t.Helper()
	r, err := gitsync.Pull(gctx(t), m.pool, c)
	if err != nil || len(r.Errors) > 0 {
		t.Fatalf("Pull %s: %v %v", m.id, err, r.Errors)
	}
	return r
}

func converged(t *testing.T, a, b *machine) {
	t.Helper()
	ra, rb := a.memRows(t), b.memRows(t)
	if !reflect.DeepEqual(ra, rb) {
		t.Errorf("memories differ:\nA: %q\nB: %q", ra, rb)
	}
	xa, xb := a.relRows(t), b.relRows(t)
	if !reflect.DeepEqual(xa, xb) {
		t.Errorf("relations differ:\nA: %q\nB: %q", xa, xb)
	}
}

// PROBE S6(c)/S7/S8: real git flow where each machine does pull then push (the order S8 names).
func TestProbePullAntesDePushEdicionesConcurrentes(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	a, b := newMachine(t, "maquina-a", "claude-code"), newMachine(t, "maquina-b", "opencode")
	ca, cb := cfgFor(t, bare, a.id), cfgFor(t, bare, b.id)
	sharedKey(t, ca, cb)
	for _, c := range []gitsync.Config{ca, cb} {
		if _, err := gitsync.Init(gctx(t), c); err != nil {
			t.Fatal(err)
		}
	}
	sync := func(m *machine, c gitsync.Config) gitsync.ImportResult { r := pull(t, m, c); push(t, m, c); return r }

	idA := a.rem(t, "Marcador visible", "plaintextmarker contenido inicial", memory.KindFact, "", "tema-secreto")
	sync(a, ca)
	sync(b, cb)
	sid := a.syncID(t, idA)
	idB := b.idBySync(t, sid)

	a.revise(t, memory.Revision{ID: idA, Action: "update", Content: "plaintextmarker version de a"})
	time.Sleep(pause)
	b.revise(t, memory.Revision{ID: idB, Action: "update", Content: "plaintextmarker version de b"})
	sync(b, cb) // B pushes its edit
	sync(a, ca) // A pulls (conflict) then pushes
	sync(b, cb)
	sync(a, ca)
	for _, m := range []*machine{a, b} {
		t.Logf("%s rows: %q", m.id, m.memRows(t))
		if n := m.count(t, "content = 'plaintextmarker version de a' AND status = 'superseded'"); n != 1 {
			t.Errorf("S6(c): %s has %d superseded copies of A's losing version, want 1", m.id, n)
		}
	}
	converged(t, a, b)

	// Same key on both machines (pull then push).
	a.rem(t, "Matrícula", "plaintextmarker matrícula 1111", memory.KindFact, "coche-matricula")
	time.Sleep(pause)
	b.rem(t, "Matrícula", "plaintextmarker matrícula 2222", memory.KindFact, "coche-matricula")
	r1 := sync(a, ca)
	r2 := sync(b, cb)
	r3 := sync(a, ca)
	t.Logf("contradictions: a1=%v b=%v a2=%v", r1.Contradictions, r2.Contradictions, r3.Contradictions)
	if len(r1.Contradictions)+len(r2.Contradictions)+len(r3.Contradictions) == 0 {
		t.Error("S6(d): no contradiction reported")
	}
	converged(t, a, b)

	// Deletion wins.
	win := a.idBySync(t, sid)
	a.revise(t, memory.Revision{ID: win, Action: "delete", Confirm: true})
	sync(a, ca)
	sync(b, cb)
	if n := b.count(t, "sync_id = $1::uuid", sid); n != 0 {
		t.Errorf("S6(f): deleted record still on B")
	}

	log := gitAll(t, bare)
	for _, s := range []string{"plaintextmarker", "Marcador visible", "tema-secreto", "claude-code", "opencode", "coche-matricula"} {
		if strings.Contains(log, s) {
			t.Errorf("S4: %q readable in the remote history", s)
		}
	}
}
