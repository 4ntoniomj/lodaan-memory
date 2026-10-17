package gitsync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/gitsync"
	"lodan/internal/memory"
	"lodan/internal/session"
	"lodan/internal/topic"
)

const (
	testDims = 64
	opTime   = 120 * time.Second
	// pause separates "older" from "newer" edits: the database stamps them with now().
	pause = 60 * time.Millisecond
)

// machine is one simulated computer: its own database and its own services.
type machine struct {
	id   string
	pool *pgxpool.Pool
	svc  *memory.Service
	tr   *session.Tracker
}

func newMachine(t *testing.T, id, client string) *machine {
	t.Helper()
	tc := database.NewTestCluster(t, testDims)
	emb := &embedding.FakeEmbedder{Dimensions: testDims}
	mgr := session.NewManager(tc.Pool, 30*time.Minute)
	topics := topic.NewResolver(tc.Pool, emb, tc.Cfg.TopicSimilarity)
	svc := memory.NewService(tc.Pool, emb, topics, mgr, tc.Cfg.DupSimilarity, 30*time.Second)
	return &machine{id: id, pool: tc.Pool, svc: svc, tr: mgr.NewTracker(client)}
}

// pair is machines A and B sharing one repository working copy and one age key.
type pair struct {
	a, b      *machine
	dir       string
	recipient string
	identity  string
	ageID     *age.X25519Identity
}

func newPair(t *testing.T) *pair {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return &pair{
		a:         newMachine(t, "maquina-a", "cliente-a"),
		b:         newMachine(t, "maquina-b", "cliente-b"),
		dir:       t.TempDir(),
		recipient: id.Recipient().String(),
		identity:  id.String(),
		ageID:     id,
	}
}

func (p *pair) export(t *testing.T, m *machine) gitsync.ExportResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTime)
	defer cancel()
	res, err := gitsync.Export(ctx, m.pool, gitsync.ExportOptions{Dir: p.dir, MachineID: m.id, Recipient: p.recipient})
	if err != nil {
		t.Fatalf("Export de %s: %v", m.id, err)
	}
	return res
}

func (p *pair) import_(t *testing.T, m *machine) gitsync.ImportResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTime)
	defer cancel()
	res, err := gitsync.Import(ctx, m.pool, gitsync.ImportOptions{Dir: p.dir, MachineID: m.id, Identity: p.identity})
	if err != nil {
		t.Fatalf("Import de %s: %v", m.id, err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("Import de %s con errores: %v", m.id, res.Errors)
	}
	return res
}

// exchange: both machines export, then both import what the other wrote.
func (p *pair) exchange(t *testing.T) (importA, importB gitsync.ImportResult) {
	t.Helper()
	p.export(t, p.a)
	p.export(t, p.b)
	importA = p.import_(t, p.a)
	importB = p.import_(t, p.b)
	return importA, importB
}

func (m *machine) rem(t *testing.T, title, content string, kind memory.Kind, key string, topics ...string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTime)
	defer cancel()
	saved, err := m.svc.Remember(ctx, m.tr, []memory.Item{{Title: title, Content: content, Kind: kind, Topics: topics, Key: key}})
	if err != nil || len(saved) != 1 {
		t.Fatalf("Remember en %s: %v (%d resultados)", m.id, err, len(saved))
	}
	return saved[0].ID
}

func (m *machine) revise(t *testing.T, r memory.Revision) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTime)
	defer cancel()
	if _, err := m.svc.Revise(ctx, m.tr, r); err != nil {
		t.Fatalf("Revise %s en %s: %v", r.Action, m.id, err)
	}
}

func (m *machine) full(t *testing.T, id int64) memory.Full {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTime)
	defer cancel()
	got, err := m.svc.Get(ctx, []int64{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("Get #%d en %s: %v (%d resultados)", id, m.id, err, len(got))
	}
	return got[0]
}

func (m *machine) syncID(t *testing.T, id int64) string {
	t.Helper()
	var s string
	if err := m.pool.QueryRow(context.Background(), `SELECT sync_id::text FROM memories WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("sync_id de #%d en %s: %v", id, m.id, err)
	}
	return s
}

func (m *machine) idBySync(t *testing.T, sync string) int64 {
	t.Helper()
	var id int64
	if err := m.pool.QueryRow(context.Background(), `SELECT id FROM memories WHERE sync_id = $1::uuid`, sync).Scan(&id); err != nil {
		t.Fatalf("registro con sync_id %s no existe en %s: %v", sync, m.id, err)
	}
	return id
}

// idByContent returns the id of the only record with that content and status.
func (m *machine) idByContent(t *testing.T, content, status string) int64 {
	t.Helper()
	var id int64
	err := m.pool.QueryRow(context.Background(),
		`SELECT id FROM memories WHERE content = $1 AND status = $2::text::memory_status`, content, status).Scan(&id)
	if err != nil {
		t.Fatalf("en %s no hay exactamente un registro %s con contenido %q: %v", m.id, status, content, err)
	}
	return id
}

func (m *machine) count(t *testing.T, where string, args ...any) int {
	t.Helper()
	var n int
	if err := m.pool.QueryRow(context.Background(), `SELECT count(*) FROM memories WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatalf("count en %s: %v", m.id, err)
	}
	return n
}

// memRows is a machine-independent view of all memories: status|key|title|content.
func (m *machine) memRows(t *testing.T) []string {
	t.Helper()
	rows, err := m.pool.Query(context.Background(),
		`SELECT status::text, coalesce(key,''), title, content FROM memories`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s, k, ti, c string
		if err := rows.Scan(&s, &k, &ti, &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s|%s|%s|%s", s, k, ti, c))
	}
	sort.Strings(out)
	return out
}

// relRows is a machine-independent view of all relations: sourceContent>targetContent|kind|state.
func (m *machine) relRows(t *testing.T) []string {
	t.Helper()
	rows, err := m.pool.Query(context.Background(),
		`SELECT s.content, g.content, r.kind::text, r.state::text FROM memory_relations r
		 JOIN memories s ON s.id = r.source_id JOIN memories g ON g.id = r.target_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, b, k, st string
		if err := rows.Scan(&a, &b, &k, &st); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s>%s|%s|%s", a, b, k, st))
	}
	sort.Strings(out)
	return out
}

func (m *machine) relState(t *testing.T, a, b int64, kind string) (string, bool) {
	t.Helper()
	var st string
	err := m.pool.QueryRow(context.Background(),
		`SELECT state::text FROM memory_relations WHERE kind = $3::text::relation_kind
		 AND ((source_id=$1 AND target_id=$2) OR (source_id=$2 AND target_id=$1))`, a, b, kind).Scan(&st)
	if err != nil {
		return "", false
	}
	return st, true
}

func requireConverged(t *testing.T, p *pair) {
	t.Helper()
	ra, rb := p.a.memRows(t), p.b.memRows(t)
	if !reflect.DeepEqual(ra, rb) {
		t.Errorf("las máquinas no convergen en memorias:\nA: %q\nB: %q", ra, rb)
	}
	xa, xb := p.a.relRows(t), p.b.relRows(t)
	if !reflect.DeepEqual(xa, xb) {
		t.Errorf("las máquinas no convergen en relaciones:\nA: %q\nB: %q", xa, xb)
	}
}

// seed creates one record on A, syncs it to B and returns the local ids and the sync_id.
func seed(t *testing.T, p *pair, title, content string) (idA, idB int64, sync string) {
	t.Helper()
	idA = p.a.rem(t, title, content, memory.KindFact, "")
	p.exchange(t)
	sync = p.a.syncID(t, idA)
	idB = p.b.idBySync(t, sync)
	return idA, idB, sync
}

func decryptFile(t *testing.T, p *pair, rel string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(p.dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(bytes.NewReader(raw), p.ageID)
	if err != nil {
		t.Fatalf("no se pudo descifrar %s en el test: %v", rel, err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func walkJSON(v any, key string, fn func(key string, val any)) {
	fn(key, v)
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			walkJSON(c, k, fn)
		}
	case []any:
		for _, c := range x {
			walkJSON(c, key, fn)
		}
	}
}

func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// --- S1, S2 ---

func TestIdentidadGlobalSyncID(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()

	var typ, nullable string
	if err := p.a.pool.QueryRow(ctx, `SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_name = 'memories' AND column_name = 'sync_id'`).Scan(&typ, &nullable); err != nil {
		t.Fatalf("memories.sync_id no existe: %v", err)
	}
	if typ != "uuid" || nullable != "NO" {
		t.Errorf("sync_id: tipo %s, nullable %s; quería uuid NOT NULL", typ, nullable)
	}

	ids := []int64{
		p.a.rem(t, "Primero", "alfa contenido uno", memory.KindFact, ""),
		p.a.rem(t, "Segundo", "bravo contenido dos", memory.KindNote, ""),
		p.a.rem(t, "Tercero", "charlie contenido tres", memory.KindEvent, ""),
	}
	seen := map[string]bool{}
	for _, id := range ids {
		s := p.a.syncID(t, id)
		if s == "" || seen[s] {
			t.Errorf("sync_id vacío o repetido: %q", s)
		}
		seen[s] = true
	}
	// unique: forcing a duplicate must fail
	if _, err := p.a.pool.Exec(ctx, `UPDATE memories SET sync_id = (SELECT sync_id FROM memories WHERE id = $1) WHERE id = $2`, ids[0], ids[1]); err == nil {
		t.Error("sync_id debería ser único")
	}

	p.exchange(t)
	for _, id := range ids {
		s := p.a.syncID(t, id)
		bid := p.b.idBySync(t, s)
		if got, want := p.b.full(t, bid).Title, p.a.full(t, id).Title; got != want {
			t.Errorf("el registro %s llegó a B con título %q, quería %q", s, got, want)
		}
	}
	if n := p.b.count(t, "true"); n != 3 {
		t.Errorf("B tiene %d registros, quería 3", n)
	}
}

func TestLosIDsLocalesNoViajan(t *testing.T) {
	p := newPair(t)
	a1 := p.a.rem(t, "Uno", "delta contenido uno", memory.KindFact, "")
	a2 := p.a.rem(t, "Dos", "echo contenido dos", memory.KindFact, "")
	p.a.revise(t, memory.Revision{ID: a1, Action: "relate", WithID: a2, Relation: "related"})
	res := p.export(t, p.a)

	plain := decryptFile(t, p, res.File)
	lines := strings.Split(strings.TrimSpace(string(plain)), "\n")
	for _, id := range []int64{a1, a2} {
		sync := p.a.syncID(t, id)
		if !strings.Contains(string(plain), sync) {
			t.Errorf("el sync_id %s no aparece en el fichero", sync)
		}
	}
	for i, line := range lines {
		var v any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("línea %d no es JSON: %v", i, err)
		}
		walkJSON(v, "", func(key string, val any) {
			if _, isNum := val.(float64); isNum && (key == "id" || strings.HasSuffix(key, "_id")) {
				t.Errorf("línea %d: el campo %q lleva un id numérico local (%v)", i, key, val)
			}
		})
	}
}

// --- S3 ---

func TestExportSoloLoNuevoYUnaVez(t *testing.T) {
	p := newPair(t)
	a1 := p.a.rem(t, "Uno", "foxtrot contenido uno", memory.KindFact, "", "tema-uno")
	a2 := p.a.rem(t, "Dos", "golf contenido dos", memory.KindFact, "", "tema-uno")
	p.a.rem(t, "Tres", "hotel contenido tres", memory.KindFact, "")
	p.a.revise(t, memory.Revision{ID: a1, Action: "relate", WithID: a2, Relation: "related"})

	var rels int
	if err := p.a.pool.QueryRow(context.Background(), `SELECT count(*) FROM memory_relations`).Scan(&rels); err != nil {
		t.Fatal(err)
	}
	first := p.export(t, p.a)
	if first.File == "" {
		t.Fatal("el primer Export debería escribir un fichero")
	}
	if first.Memories != 3 || first.Relations != rels || first.Sessions != 1 || first.Deletions != 0 {
		t.Errorf("recuento del primer Export = %+v; quería 3 memorias, %d relaciones, 1 sesión, 0 borrados", first, rels)
	}
	filesAfterFirst := filesUnder(t, p.dir)

	second := p.export(t, p.a)
	if second.File != "" || second.Memories+second.Relations+second.Sessions+second.Deletions != 0 {
		t.Errorf("el segundo Export sin cambios = %+v; quería File vacío y todo a 0", second)
	}
	if got := filesUnder(t, p.dir); !reflect.DeepEqual(got, filesAfterFirst) {
		t.Errorf("el segundo Export escribió ficheros: antes %v, después %v", filesAfterFirst, got)
	}

	// only the changed record travels
	p.a.revise(t, memory.Revision{ID: a1, Action: "update", Content: "foxtrot contenido uno editado"})
	third := p.export(t, p.a)
	if third.File == "" || third.Memories != 1 || third.Relations != 0 || third.Deletions != 0 {
		t.Errorf("Export tras editar un registro = %+v; quería 1 memoria, 0 relaciones, 0 borrados", third)
	}

	// a physical deletion is exported as a deletion
	p.a.revise(t, memory.Revision{ID: a2, Action: "delete", Confirm: true})
	fourth := p.export(t, p.a)
	if fourth.File == "" || fourth.Deletions != 1 || fourth.Memories != 0 {
		t.Errorf("Export tras borrar = %+v; quería 1 borrado y 0 memorias", fourth)
	}
}

func TestLoImportadoNoSeReexporta(t *testing.T) {
	p := newPair(t)
	p.a.rem(t, "Uno", "india contenido uno", memory.KindFact, "")
	p.a.rem(t, "Dos", "juliet contenido dos", memory.KindNote, "")
	p.export(t, p.a)
	imp := p.import_(t, p.b)
	if imp.Files != 1 {
		t.Fatalf("B importó %d ficheros, quería 1", imp.Files)
	}
	if res := p.export(t, p.b); res.File != "" {
		t.Errorf("B reexportó lo importado: %+v", res)
	}
	// and A skips its own folder
	if res := p.import_(t, p.a); res.Files != 0 {
		t.Errorf("A importó %d ficheros de su propia carpeta, quería 0", res.Files)
	}
}

// --- S4 ---

func TestFormatoYCifrado(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	const (
		title   = "Titulo-secreto-zxq"
		content = "contenido-secreto-zxq kilo lima mike"
		slug    = "xilofono-raro"
	)
	id := p.a.rem(t, title, content, memory.KindDecision, "", slug)
	if _, err := p.a.svc.FillPending(ctx, 100); err != nil {
		t.Fatal(err)
	}
	slugs := p.a.full(t, id).Topics
	if len(slugs) == 0 {
		t.Fatal("el registro no tiene temas")
	}
	p.a.tr.End(ctx, "resumen-secreto-zxq")

	res := p.export(t, p.a)
	if !regexp.MustCompile(`^changes/maquina-a/\d{8}T\d{6}Z-\d+\.jsonl\.age$`).MatchString(res.File) {
		t.Fatalf("ruta del fichero %q no cumple el formato", res.File)
	}
	raw, err := os.ReadFile(filepath.Join(p.dir, filepath.FromSlash(res.File)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) {
		t.Errorf("el fichero no empieza por age-encryption.org/v1")
	}
	secrets := append([]string{title, content, "cliente-a", "resumen-secreto-zxq"}, slugs...)
	for _, s := range secrets {
		if bytes.Contains(raw, []byte(s)) {
			t.Errorf("el texto %q aparece legible en el fichero", s)
		}
	}

	plain := decryptFile(t, p, res.File)
	lines := strings.Split(strings.TrimSpace(string(plain)), "\n")
	if len(lines) < 2 {
		t.Fatalf("el fichero tiene %d líneas, quería cabecera y cambios", len(lines))
	}
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("la cabecera no es JSON: %v", err)
	}
	if !strings.Contains(lines[0], `"format":"lodan-sync/1"`) && header["format"] != "lodan-sync/1" {
		t.Errorf("cabecera sin format lodan-sync/1: %s", lines[0])
	}
	if !strings.Contains(lines[0], "maquina-a") {
		t.Errorf("cabecera sin el id de máquina: %s", lines[0])
	}
	for i, line := range lines {
		var v any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("línea %d no es JSON: %v", i, err)
		}
		walkJSON(v, "", func(key string, _ any) {
			if strings.Contains(strings.ToLower(key), "embedding") || key == "tsv" {
				t.Errorf("línea %d lleva el campo %q que no debe exportarse", i, key)
			}
		})
	}
	for _, banned := range []string{"lodan_settings", "AGE-SECRET-KEY"} {
		if strings.Contains(string(plain), banned) {
			t.Errorf("el fichero descifrado contiene %q", banned)
		}
	}

	// each machine writes only to its own folder
	p.b.rem(t, "De B", "november contenido de b", memory.KindFact, "")
	p.exchange(t)
	for _, f := range filesUnder(t, p.dir) {
		if !strings.HasPrefix(f, "changes/maquina-a/") && !strings.HasPrefix(f, "changes/maquina-b/") {
			t.Errorf("fichero fuera de las carpetas de las máquinas: %s", f)
		}
	}
	if n := len(filesUnder(t, filepath.Join(p.dir, "changes", "maquina-b"))); n != 1 {
		t.Errorf("la carpeta de B tiene %d ficheros, quería 1", n)
	}
}

// --- S5 ---

func TestImportCreaRegistrosSinEmbedding(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	occurred := time.Date(2025, 3, 4, 10, 20, 30, 0, time.UTC)
	saved, err := p.a.svc.Remember(ctx, p.a.tr, []memory.Item{{
		Title: "Cumpleaños", Content: "papa golf oscar", Kind: memory.KindEvent,
		Topics: []string{"familia", "fechas"}, Key: "cumple-clave", OccurredAt: &occurred,
	}})
	if err != nil {
		t.Fatal(err)
	}
	idA := saved[0].ID
	p.exchange(t)

	idB := p.b.idBySync(t, p.a.syncID(t, idA))
	fa, fb := p.a.full(t, idA), p.b.full(t, idB)
	if fa.Title != fb.Title || fa.Content != fb.Content || fa.Kind != fb.Kind || fa.Status != fb.Status ||
		fa.Key != fb.Key || !reflect.DeepEqual(fa.Topics, fb.Topics) || !fa.OccurredAt.Equal(fb.OccurredAt) {
		t.Errorf("registro importado distinto:\nA: %+v\nB: %+v", fa, fb)
	}
	if !fb.OccurredAt.Equal(occurred) {
		t.Errorf("occurred_at = %v, quería %v", fb.OccurredAt, occurred)
	}

	var nullEmb bool
	if err := p.b.pool.QueryRow(ctx, `SELECT embedding IS NULL FROM memories WHERE id = $1`, idB).Scan(&nullEmb); err != nil {
		t.Fatal(err)
	}
	if !nullEmb {
		t.Error("el registro importado debería llegar sin embedding")
	}
	if _, err := p.b.svc.FillPending(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := p.b.pool.QueryRow(ctx, `SELECT embedding IS NULL FROM memories WHERE id = $1`, idB).Scan(&nullEmb); err != nil {
		t.Fatal(err)
	}
	if nullEmb {
		t.Error("FillPending debería calcular el embedding del registro importado")
	}
}

func TestImportIdempotente(t *testing.T) {
	p := newPair(t)
	p.a.rem(t, "Uno", "papa contenido uno", memory.KindFact, "", "tema-x")
	p.a.rem(t, "Dos", "quebec contenido dos", memory.KindNote, "")
	p.export(t, p.a)
	if res := p.import_(t, p.b); res.Files != 1 {
		t.Fatalf("primer Import: %d ficheros, quería 1", res.Files)
	}
	before := p.b.memRows(t)
	again := p.import_(t, p.b)
	if again.Files != 0 {
		t.Errorf("segundo Import aplicó %d ficheros, quería 0", again.Files)
	}
	if after := p.b.memRows(t); !reflect.DeepEqual(before, after) {
		t.Errorf("el segundo Import cambió los datos:\nantes %q\ndespués %q", before, after)
	}
	if n := p.b.count(t, "true"); n != 2 {
		t.Errorf("B tiene %d registros, quería 2", n)
	}
}

func TestImportDeMasAntiguoAMasNuevo(t *testing.T) {
	p := newPair(t)
	id := p.a.rem(t, "Versionado", "romeo version uno", memory.KindFact, "")
	p.export(t, p.a)
	p.a.revise(t, memory.Revision{ID: id, Action: "update", Content: "romeo version dos"})
	p.export(t, p.a)
	res := p.import_(t, p.b)
	if res.Files != 2 {
		t.Fatalf("Import aplicó %d ficheros, quería 2", res.Files)
	}
	if n := p.b.count(t, "true"); n != 1 {
		t.Errorf("B tiene %d registros, quería 1", n)
	}
	p.b.idByContent(t, "romeo version dos", "active")
}

func TestImportFicheroConOtraClave(t *testing.T) {
	p := newPair(t)
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// B writes a valid file (key of the pair) and then a file encrypted to another key,
	// under a different machine folder; A imports both in one run.
	p.b.rem(t, "Valido", "sierra contenido valido", memory.KindFact, "")
	p.export(t, p.b)
	p.b.rem(t, "Ilegible", "tango contenido ilegible", memory.KindFact, "")
	bad, err := gitsync.Export(ctx, p.b.pool, gitsync.ExportOptions{Dir: p.dir, MachineID: "maquina-c", Recipient: other.Recipient().String()})
	if err != nil || bad.File == "" {
		t.Fatalf("Export con otra clave: %+v, %v", bad, err)
	}

	res, err := gitsync.Import(ctx, p.a.pool, gitsync.ImportOptions{Dir: p.dir, MachineID: p.a.id, Identity: p.identity})
	if err != nil {
		t.Fatalf("Import no debe devolver error fatal por un fichero ilegible: %v", err)
	}
	want := "no se pudo descifrar " + bad.File + ": la clave no coincide"
	if len(res.Errors) != 1 || res.Errors[0] != want {
		t.Errorf("Errors = %q; quería exactamente [%q]", res.Errors, want)
	}
	if res.Files != 1 {
		t.Errorf("Files = %d, quería 1 (solo el válido)", res.Files)
	}
	if n := p.a.count(t, "content = $1", "sierra contenido valido"); n != 1 {
		t.Error("el fichero válido de otra máquina debería haberse aplicado")
	}
	if n := p.a.count(t, "content = $1", "tango contenido ilegible"); n != 0 {
		t.Error("el fichero ilegible no debería haberse aplicado")
	}
}

// --- S6 ---

func TestMergeA_RegistroDesconocido(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	old := p.a.rem(t, "Viejo", "uniform contenido viejo", memory.KindFact, "")
	new_ := p.a.rem(t, "Nuevo", "victor contenido nuevo", memory.KindDecision, "", "viajes")
	gone := p.a.rem(t, "Anulado", "whiskey contenido anulado", memory.KindNote, "")
	p.a.revise(t, memory.Revision{ID: old, Action: "supersede", WithID: new_})
	p.a.revise(t, memory.Revision{ID: gone, Action: "invalidate"})
	p.exchange(t)

	for _, c := range []struct{ content, status string }{
		{"uniform contenido viejo", "superseded"},
		{"victor contenido nuevo", "active"},
		{"whiskey contenido anulado", "invalidated"},
	} {
		if p.b.count(t, "content = $1 AND status = $2::text::memory_status", c.content, c.status) != 1 {
			t.Errorf("B no tiene %q con estado %s", c.content, c.status)
		}
	}
	idB := p.b.idByContent(t, "victor contenido nuevo", "active")
	fa, fb := p.a.full(t, new_), p.b.full(t, idB)
	if !reflect.DeepEqual(fa.Topics, fb.Topics) || fb.SessionID != fa.SessionID || fb.SessionID == "" {
		t.Errorf("temas o sesión distintos: A %v/%q, B %v/%q", fa.Topics, fa.SessionID, fb.Topics, fb.SessionID)
	}
	if !fa.OccurredAt.Equal(fb.OccurredAt) || !fa.CreatedAt.Equal(fb.CreatedAt) {
		t.Errorf("fechas distintas: A %v/%v, B %v/%v", fa.OccurredAt, fa.CreatedAt, fb.OccurredAt, fb.CreatedAt)
	}
	var client string
	if err := p.b.pool.QueryRow(ctx, `SELECT client FROM sessions WHERE id = $1`, fb.SessionID).Scan(&client); err != nil || client != "cliente-a" {
		t.Errorf("la sesión no llegó a B con su cliente: %q, %v", client, err)
	}
	requireConverged(t, p)
}

func TestMergeB_EstadoSoloAvanza(t *testing.T) {
	p := newPair(t)
	idA, idB, sync := seed(t, p, "Dato", "xray contenido inicial")

	p.a.revise(t, memory.Revision{ID: idA, Action: "invalidate"})
	time.Sleep(pause)
	p.b.revise(t, memory.Revision{ID: idB, Action: "update", Content: "xray contenido editado en b"})
	p.exchange(t)

	for _, m := range []*machine{p.a, p.b} {
		if n := m.count(t, "status = 'active'"); n != 0 {
			t.Errorf("%s tiene %d registros activos, quería 0 (no se reactiva)", m.id, n)
		}
		if n := m.count(t, "sync_id = $1::uuid AND status = 'invalidated'", sync); n != 1 {
			t.Errorf("%s: el registro %s no quedó invalidado", m.id, sync)
		}
		if n := m.count(t, "true"); n != 1 {
			t.Errorf("%s tiene %d registros, quería 1", m.id, n)
		}
	}
}

func TestMergeC1_EdicionesConcurrentes(t *testing.T) {
	p := newPair(t)
	idA, idB, sync := seed(t, p, "Dato", "yankee contenido inicial")

	p.a.revise(t, memory.Revision{ID: idA, Action: "update", Content: "yankee version de a"})
	time.Sleep(pause)
	p.b.revise(t, memory.Revision{ID: idB, Action: "update", Content: "yankee version de b"})
	p.exchange(t)
	p.exchange(t) // nothing may be duplicated by a second round

	for _, m := range []*machine{p.a, p.b} {
		if n := m.count(t, "true"); n != 2 {
			t.Errorf("%s tiene %d registros, quería 2 (ganador + copia)", m.id, n)
		}
		win := m.idBySync(t, sync)
		if got := m.full(t, win); got.Content != "yankee version de b" || got.Status != memory.StatusActive {
			t.Errorf("%s: el ganador es %q/%s, quería la versión más reciente activa", m.id, got.Content, got.Status)
		}
		copyID := m.idByContent(t, "yankee version de a", "superseded")
		if st, ok := m.relState(t, win, copyID, "supersedes"); !ok || st != "confirmed" {
			t.Errorf("%s: falta la relación supersedes confirmada ganador->copia (ok=%v, estado=%q)", m.id, ok, st)
		}
		var src int64
		if err := m.pool.QueryRow(context.Background(),
			`SELECT source_id FROM memory_relations WHERE kind = 'supersedes' AND target_id = $1`, copyID).Scan(&src); err != nil || src != win {
			t.Errorf("%s: la copia debe ser sustituida por el ganador (origen %d, ganador %d, err %v)", m.id, src, win, err)
		}
	}
	requireConverged(t, p)
}

func TestMergeC2_EdicionSobreLaDelOtro(t *testing.T) {
	p := newPair(t)
	idA, idB, sync := seed(t, p, "Dato", "zulu contenido inicial")

	p.a.revise(t, memory.Revision{ID: idA, Action: "update", Content: "zulu version de a"})
	p.exchange(t) // B has seen A's edit
	p.b.revise(t, memory.Revision{ID: idB, Action: "update", Content: "zulu version de b sobre a"})
	p.exchange(t)

	for _, m := range []*machine{p.a, p.b} {
		if n := m.count(t, "true"); n != 1 {
			t.Errorf("%s tiene %d registros, quería 1 (sin copia)", m.id, n)
		}
		if got := m.full(t, m.idBySync(t, sync)); got.Content != "zulu version de b sobre a" || got.Status != memory.StatusActive {
			t.Errorf("%s: contenido %q/%s, quería la edición de B activa", m.id, got.Content, got.Status)
		}
	}
	requireConverged(t, p)
}

func TestMergeD_MismaClaveContradiccion(t *testing.T) {
	p := newPair(t)
	p.a.rem(t, "Matrícula", "matrícula 1111 AAA", memory.KindFact, "coche-matricula")
	time.Sleep(pause)
	p.b.rem(t, "Matrícula", "matrícula 2222 BBB", memory.KindFact, "coche-matricula")
	impA, impB := p.exchange(t)
	p.exchange(t)

	for _, c := range []struct {
		m   *machine
		imp gitsync.ImportResult
	}{{p.a, impA}, {p.b, impB}} {
		m := c.m
		if n := m.count(t, "status = 'active'"); n != 2 {
			t.Errorf("%s tiene %d activos, quería 2", m.id, n)
		}
		kept := m.idByContent(t, "matrícula 2222 BBB", "active")
		other := m.idByContent(t, "matrícula 1111 AAA", "active")
		if got := m.full(t, kept).Key; got != "coche-matricula" {
			t.Errorf("%s: el más reciente debería conservar la clave, tiene %q", m.id, got)
		}
		if got := m.full(t, other).Key; got != "" {
			t.Errorf("%s: el más antiguo no debería tener clave, tiene %q", m.id, got)
		}
		if st, ok := m.relState(t, kept, other, "contradicts"); !ok || st != "confirmed" {
			t.Errorf("%s: falta la relación contradicts confirmada (ok=%v, estado=%q)", m.id, ok, st)
		}
		if len(c.imp.Contradictions) != 1 {
			t.Errorf("%s: Contradictions = %+v, quería 1 par", m.id, c.imp.Contradictions)
			continue
		}
		got := c.imp.Contradictions[0]
		if got.Key != "coche-matricula" || got.KeptID != kept || got.OtherID != other {
			t.Errorf("%s: contradicción %+v, quería clave coche-matricula, KeptID %d, OtherID %d", m.id, got, kept, other)
		}
	}
	requireConverged(t, p)
}

func TestMergeE_ContenidoIdentico(t *testing.T) {
	p := newPair(t)
	const content = "contenido idéntico en las dos máquinas"
	idA := p.a.rem(t, "Título de A", content, memory.KindFact, "")
	p.b.rem(t, "Título de B", content, memory.KindFact, "")
	p.exchange(t)

	for _, m := range []*machine{p.a, p.b} {
		if n := m.count(t, "true"); n != 1 {
			t.Errorf("%s tiene %d registros, quería 1", m.id, n)
		}
		if n := m.count(t, "status = 'active'"); n != 1 {
			t.Errorf("%s tiene %d activos, quería 1", m.id, n)
		}
	}

	p.a.revise(t, memory.Revision{ID: idA, Action: "update", Content: "contenido idéntico, ahora actualizado"})
	p.exchange(t)
	for _, m := range []*machine{p.a, p.b} {
		if n := m.count(t, "true"); n != 1 {
			t.Errorf("%s tiene %d registros tras la edición, quería 1", m.id, n)
		}
		m.idByContent(t, "contenido idéntico, ahora actualizado", "active")
	}
	requireConverged(t, p)
}

func TestMergeF_BorradoGanaSobreEdicion(t *testing.T) {
	// delete on A reaches B
	p := newPair(t)
	idA, _, _ := seed(t, p, "Borrable", "alfa1 contenido borrable")
	p.a.revise(t, memory.Revision{ID: idA, Action: "delete", Confirm: true})
	p.exchange(t)
	p.exchange(t)
	for _, m := range []*machine{p.a, p.b} {
		if n := m.count(t, "true"); n != 0 {
			t.Errorf("%s conserva %d registros tras el borrado, quería 0", m.id, n)
		}
	}
}

func TestMergeF_BorradoConcurrenteConEdicion(t *testing.T) {
	p := newPair(t)
	idA, idB, _ := seed(t, p, "Borrable", "bravo1 contenido borrable")
	p.a.revise(t, memory.Revision{ID: idA, Action: "update", Content: "bravo1 contenido editado en a"})
	time.Sleep(pause)
	p.b.revise(t, memory.Revision{ID: idB, Action: "delete", Confirm: true})
	p.exchange(t)
	p.exchange(t)
	for _, m := range []*machine{p.a, p.b} {
		if n := m.count(t, "true"); n != 0 {
			t.Errorf("%s conserva %d registros, quería 0 (el borrado gana, sin copias)", m.id, n)
		}
	}
}

func TestMergeF_UnaCopiaPosteriorNoRecreaElBorrado(t *testing.T) {
	p := newPair(t)
	id := p.a.rem(t, "Efímero", "charlie1 contenido efímero", memory.KindFact, "")
	p.export(t, p.a) // file with the creation
	p.a.revise(t, memory.Revision{ID: id, Action: "delete", Confirm: true})
	p.export(t, p.a) // file with the deletion
	p.import_(t, p.b)
	if n := p.b.count(t, "true"); n != 0 {
		t.Errorf("B tiene %d registros, quería 0", n)
	}
	// B imports again and exchanges again: the record must not come back
	p.exchange(t)
	if n := p.b.count(t, "true"); n != 0 {
		t.Errorf("B recreó el registro borrado (%d)", n)
	}
}

func TestMergeG_Relaciones(t *testing.T) {
	p := newPair(t)
	a1 := p.a.rem(t, "Uno", "delta1 contenido uno", memory.KindFact, "")
	a2 := p.a.rem(t, "Dos", "echo1 contenido dos", memory.KindFact, "")
	p.exchange(t)

	p.a.revise(t, memory.Revision{ID: a1, Action: "relate", WithID: a2, Relation: "part_of"})
	p.exchange(t)
	b1 := p.b.idByContent(t, "delta1 contenido uno", "active")
	b2 := p.b.idByContent(t, "echo1 contenido dos", "active")
	if st, ok := p.b.relState(t, b1, b2, "part_of"); !ok || st != "confirmed" {
		t.Fatalf("la relación creada en A no llegó a B confirmada (ok=%v, estado=%q)", ok, st)
	}
	requireConverged(t, p)

	time.Sleep(pause)
	p.b.revise(t, memory.Revision{ID: b1, Action: "reject_relation", WithID: b2, Relation: "part_of"})
	p.exchange(t)
	if st, ok := p.a.relState(t, a1, a2, "part_of"); !ok || st != "rejected" {
		t.Errorf("el rechazo hecho en B no llegó a A (ok=%v, estado=%q)", ok, st)
	}
	requireConverged(t, p)
}

func TestMergeH_Sesiones(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	idA := p.a.rem(t, "Con sesión", "foxtrot1 contenido sesión", memory.KindFact, "", "proyecto-sesion")
	sessionID := p.a.full(t, idA).SessionID
	if sessionID == "" {
		t.Fatal("el registro no tiene sesión")
	}
	p.exchange(t)

	type sess struct {
		client  string
		ended   *time.Time
		summary *string
	}
	read := func(m *machine) sess {
		var s sess
		if err := m.pool.QueryRow(ctx, `SELECT client, ended_at, summary FROM sessions WHERE id = $1`, sessionID).Scan(&s.client, &s.ended, &s.summary); err != nil {
			t.Fatalf("sesión %s no existe en %s: %v", sessionID, m.id, err)
		}
		return s
	}
	if s := read(p.b); s.client != "cliente-a" || s.ended != nil {
		t.Errorf("sesión abierta en B: %+v, quería cliente-a y sin cierre", s)
	}

	if _, err := p.a.tr.End(ctx, "resumen de la sesión"); err != nil {
		t.Fatal(err)
	}
	p.exchange(t)

	sa, sb := read(p.a), read(p.b)
	if sb.ended == nil || sb.summary == nil || *sb.summary != "resumen de la sesión" {
		t.Fatalf("B no recibió el cierre y el resumen: %+v", sb)
	}
	if !sa.ended.Equal(*sb.ended) {
		t.Errorf("ended_at distinto: A %v, B %v", sa.ended, sb.ended)
	}
	topicsOf := func(m *machine) []string {
		rows, err := m.pool.Query(ctx, `SELECT t.slug FROM session_topics st JOIN topics t ON t.id = st.topic_id WHERE st.session_id = $1`, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		sort.Strings(out)
		return out
	}
	ta, tb := topicsOf(p.a), topicsOf(p.b)
	if len(ta) == 0 || !reflect.DeepEqual(ta, tb) {
		t.Errorf("temas de la sesión: A %v, B %v; quería los mismos y no vacíos", ta, tb)
	}
}

func copyFolder(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// S6(g): a relation whose records have not arrived yet is applied when they arrive.
func TestMergeG_RelacionPendienteHastaQueLleganLosRegistros(t *testing.T) {
	p := newPair(t)
	c := newMachine(t, "maquina-c", "cliente-c")
	dirC := t.TempDir()
	ctx := context.Background()

	// A creates a record; B imports it, creates its own and relates it to A's.
	p.a.rem(t, "De A", "gamma2 contenido de a", memory.KindFact, "")
	p.export(t, p.a)
	p.import_(t, p.b)
	idB := p.b.rem(t, "De B", "hotel2 contenido de b", memory.KindFact, "")
	idAonB := p.b.idByContent(t, "gamma2 contenido de a", "active")
	p.b.revise(t, memory.Revision{ID: idB, Action: "relate", WithID: idAonB, Relation: "related"})
	resB := p.export(t, p.b)
	if resB.Relations != 1 {
		t.Fatalf("el fichero de B debería llevar 1 relación, lleva %d", resB.Relations)
	}

	// C only sees B's folder first.
	copyFolder(t, filepath.Join(p.dir, "changes", "maquina-b"), filepath.Join(dirC, "changes", "maquina-b"))
	imp := func() gitsync.ImportResult {
		res, err := gitsync.Import(ctx, c.pool, gitsync.ImportOptions{Dir: dirC, MachineID: c.id, Identity: p.identity})
		if err != nil || len(res.Errors) != 0 {
			t.Fatalf("Import en C: %v %v", err, res.Errors)
		}
		return res
	}
	if res := imp(); res.Files != 1 {
		t.Fatalf("primer Import en C: %d ficheros, quería 1", res.Files)
	}
	var rels int
	if err := c.pool.QueryRow(ctx, `SELECT count(*) FROM memory_relations`).Scan(&rels); err != nil {
		t.Fatal(err)
	}
	if rels != 0 {
		t.Errorf("C tiene %d relaciones antes de recibir el registro de A, quería 0", rels)
	}

	// Now A's file appears.
	copyFolder(t, filepath.Join(p.dir, "changes", "maquina-a"), filepath.Join(dirC, "changes", "maquina-a"))
	if res := imp(); res.Files != 1 {
		t.Fatalf("segundo Import en C: %d ficheros, quería 1", res.Files)
	}
	cA := c.idByContent(t, "gamma2 contenido de a", "active")
	cB := c.idByContent(t, "hotel2 contenido de b", "active")
	if st, ok := c.relState(t, cB, cA, "related"); !ok || st != "confirmed" {
		t.Errorf("la relación pendiente no se aplicó en C (ok=%v, estado=%q)", ok, st)
	}
	var src int64
	if err := c.pool.QueryRow(ctx, `SELECT source_id FROM memory_relations WHERE kind='related'`).Scan(&src); err != nil || src != cB {
		t.Errorf("la relación debe ir de B a A (origen %d, quería %d, err %v)", src, cB, err)
	}
}
