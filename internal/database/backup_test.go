package database

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/klauspost/compress/zstd"
)

// backupTestTimeout bounds each backup or restore performed by these tests.
const backupTestTimeout = 5 * time.Minute

// backupCtx returns a context that is cancelled when the test ends.
func backupCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), backupTestTimeout)
	t.Cleanup(cancel)
	return ctx
}

// newBackupCluster returns an initialized cluster over a temporary data directory (never the
// user's one) that is stopped when the test ends. With start it is also running and has the
// "lodan" database. The test is skipped if the PostgreSQL binaries are missing.
func newBackupCluster(t *testing.T, start bool) *Cluster {
	t.Helper()
	cl, _ := newLocalCluster(t, 4)
	ctx := backupCtx(t)
	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init falló: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		if err := cl.Stop(stopCtx); err != nil {
			t.Logf("no se pudo parar el clúster de prueba: %v", err)
		}
	})
	if start {
		if err := cl.Start(ctx); err != nil {
			t.Fatalf("Start falló: %v", err)
		}
		if err := cl.EnsureDatabase(ctx, defaultDatabase); err != nil {
			t.Fatalf("EnsureDatabase falló: %v", err)
		}
	}
	return cl
}

// sqlConnect opens a connection to the "lodan" database of cl.
func sqlConnect(ctx context.Context, t *testing.T, cl *Cluster) *pgx.Conn {
	t.Helper()
	dsn, err := cl.DSN(defaultDatabase)
	if err != nil {
		t.Fatalf("DSN falló: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("no se pudo conectar a PostgreSQL: %v", err)
	}
	return conn
}

// sqlExec runs one statement on a short-lived connection (a backup or restore stops the server,
// so no connection is kept between operations).
func sqlExec(t *testing.T, cl *Cluster, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	conn := sqlConnect(ctx, t, cl)
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, query, args...); err != nil {
		t.Fatalf("%s falló: %v", query, err)
	}
}

// sqlColumn runs a query that returns one text column and returns its values.
func sqlColumn(t *testing.T, cl *Cluster, query string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	conn := sqlConnect(ctx, t, cl)
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, query)
	if err != nil {
		t.Fatalf("%s falló: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("%s: no se pudo leer la fila: %v", query, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s falló: %v", query, err)
	}
	return out
}

// assertRunning checks whether PostgreSQL is running.
func assertRunning(ctx context.Context, t *testing.T, cl *Cluster, want bool) {
	t.Helper()
	running, err := cl.Status(ctx)
	if err != nil {
		t.Fatalf("Status falló: %v", err)
	}
	if running != want {
		t.Fatalf("PostgreSQL en marcha = %v, se esperaba %v", running, want)
	}
}

// assertSteps checks the rows of the chain table, sorted.
func assertSteps(t *testing.T, cl *Cluster, want ...string) {
	t.Helper()
	got := sqlColumn(t, cl, "SELECT step FROM chain ORDER BY step")
	if !slices.Equal(got, want) {
		t.Fatalf("filas de chain = %v, se esperaba %v", got, want)
	}
}

// waitForNextSecond sleeps until the clock is in a later second than the creation of the backup
// at path: two backups of the same kind in the same second would get the same file name (and
// the code refuses to overwrite). It sleeps one second at most.
func waitForNextSecond(t *testing.T, path string) {
	t.Helper()
	info, err := readBackupInfo(path)
	if err != nil {
		t.Fatalf("readBackupInfo falló: %v", err)
	}
	if d := time.Until(info.CreatedAt.Truncate(time.Second).Add(time.Second)); d > 0 {
		time.Sleep(d + 10*time.Millisecond)
	}
}

// makeBackup makes a backup of the given type in dir, checks its name and returns its path and
// info. It then waits for the next second so that the next backup can be made right away.
func makeBackup(ctx context.Context, t *testing.T, cl *Cluster, dir, backupType string) (string, backupInfo) {
	t.Helper()
	path, err := cl.Backup(ctx, dir, backupType)
	if err != nil {
		t.Fatalf("Backup(%s) falló: %v", backupType, err)
	}
	if !backupNameRe.MatchString(filepath.Base(path)) {
		t.Fatalf("el nombre del backup %q no cumple el formato esperado", filepath.Base(path))
	}
	info, err := readBackupInfo(path)
	if err != nil {
		t.Fatalf("readBackupInfo(%s) falló: %v", path, err)
	}
	waitForNextSecond(t, path)
	return path, info
}

// restoreAndClean restores files and deletes the previous cluster that Restore keeps, whose name
// has a one-second resolution: two restores in a row must not find it already there.
func restoreAndClean(ctx context.Context, t *testing.T, cl *Cluster, files []string) {
	t.Helper()
	previous, err := cl.Restore(ctx, files)
	if err != nil {
		t.Fatalf("Restore falló: %v", err)
	}
	if previous == "" {
		t.Fatal("Restore debería devolver el clúster anterior que ha conservado")
	}
	if _, err := os.Lstat(previous); err != nil {
		t.Fatalf("el clúster anterior %s no existe: %v", previous, err)
	}
	if err := os.RemoveAll(previous); err != nil {
		t.Fatalf("no se pudo borrar %s: %v", previous, err)
	}
}

// writeFakeBackup writes a small but valid backup (with a manifest and no files) with the given
// kind, creation time and reference, to test the chain logic without a PostgreSQL.
func writeFakeBackup(t *testing.T, dir, kind string, createdAt, since time.Time) string {
	t.Helper()
	info := backupInfo{Kind: kind, CreatedAt: createdAt, Since: since, PGVersion: "18"}
	manifest := renderManifest([]manifestEntry{{"PG_VERSION", false, 3}})
	path := filepath.Join(dir, backupPrefix+createdAt.Format(backupNameLayout)+"."+kind+backupExt)
	if err := writeBackupArchive(path, info, manifest, nil); err != nil {
		t.Fatalf("writeBackupArchive falló: %v", err)
	}
	return path
}

// rawEntry is a file of an archive written by writeRawBackup.
type rawEntry struct {
	name string
	text string
}

// writeRawBackup writes a tar.zst with exactly the given entries, in order.
func writeRawBackup(t *testing.T, path string, entries ...rawEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(e.text))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, e.text); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// rewriteManifest copies the backup src to dst changing only the text of its manifest.txt.
func rewriteManifest(t *testing.T, src, dst string, edit func(manifest string) string) {
	t.Helper()
	tr, closeFn, err := openBackup(src)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	zw, err := zstd.NewWriter(out)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == backupManifestFile {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			text := edit(string(data))
			h := *hdr
			h.Size = int64(len(text))
			if err := tw.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(tw, text); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeTree creates directories and files (with their content) under root.
func writeTree(t *testing.T, root string, dirs []string, files map[string]string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// 1. Full backup and restore.

func TestBackupFullIdaYVuelta(t *testing.T) {
	cl := newBackupCluster(t, true)
	ctx := backupCtx(t)
	dir := t.TempDir()

	sqlExec(t, cl, "CREATE TABLE notes (id int PRIMARY KEY, body text NOT NULL)")
	sqlExec(t, cl, "INSERT INTO notes SELECT i, 'nota ' || i FROM generate_series(1, 200) AS i")
	want := sqlColumn(t, cl, "SELECT body FROM notes ORDER BY id")
	if len(want) != 200 {
		t.Fatalf("se esperaban 200 filas y hay %d", len(want))
	}

	path, err := cl.Backup(ctx, dir, BackupFull)
	if err != nil {
		t.Fatalf("Backup falló: %v", err)
	}
	if !strings.HasSuffix(path, ".full.tar.zst") || !backupNameRe.MatchString(filepath.Base(path)) {
		t.Errorf("nombre de backup inesperado: %s", path)
	}
	assertRunning(ctx, t, cl, true) // it was running: it must be running again

	info, err := readBackupInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "full" || !info.Since.IsZero() || info.Files == 0 || info.PGVersion == "" {
		t.Errorf("info.txt inesperado: %+v", info)
	}
	manifest, err := readBackupManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	var haveVersion bool
	for _, e := range manifest {
		if e.Path == "PG_VERSION" && !e.Dir {
			haveVersion = true
		}
		if e.Path == "postmaster.pid" {
			t.Error("el manifiesto no debe incluir postmaster.pid")
		}
	}
	if !haveVersion {
		t.Error("el manifiesto no incluye PG_VERSION")
	}

	// Change the data after the backup: restoring must bring the original rows back.
	sqlExec(t, cl, "DELETE FROM notes WHERE id <= 100")
	sqlExec(t, cl, "UPDATE notes SET body = 'cambiada' WHERE id > 150")
	sqlExec(t, cl, "CREATE TABLE extra (x int)")

	restoreAndClean(ctx, t, cl, []string{path})
	assertRunning(ctx, t, cl, true)

	got := sqlColumn(t, cl, "SELECT body FROM notes ORDER BY id")
	if !slices.Equal(got, want) {
		t.Errorf("tras restaurar hay %d filas distintas de las originales (%d)", len(got), len(want))
	}
	if got := sqlColumn(t, cl, "SELECT (to_regclass('extra') IS NOT NULL)::text"); !slices.Equal(got, []string{"false"}) {
		t.Errorf("la tabla extra, creada tras el backup, debería haber desaparecido: %v", got)
	}
}

// 2. Chain with the standard semantics: full, differential, incremental, incremental.

func TestBackupCadenaConSemanticaEstandar(t *testing.T) {
	cl := newBackupCluster(t, true)
	ctx := backupCtx(t)
	dir := t.TempDir()
	step := func(name string) { sqlExec(t, cl, "INSERT INTO chain VALUES ($1)", name) }

	sqlExec(t, cl, "CREATE TABLE chain (step text)")
	step("full")
	fullPath, full := makeBackup(ctx, t, cl, dir, BackupFull)
	step("A")
	diffPath, diff := makeBackup(ctx, t, cl, dir, BackupDifferential)
	step("B")
	incr1Path, incr1 := makeBackup(ctx, t, cl, dir, BackupIncremental)
	step("C")
	incr2Path, incr2 := makeBackup(ctx, t, cl, dir, BackupIncremental)
	step("D") // after the last backup: it must not survive the restore

	if full.Kind != "full" || !full.Since.IsZero() {
		t.Errorf("full: %+v", full)
	}
	if diff.Kind != "diff" || !diff.Since.Equal(full.CreatedAt) {
		t.Errorf("diff debería partir del full (%v) y parte de %v", full.CreatedAt, diff.Since)
	}
	if incr1.Kind != "incr" || !incr1.Since.Equal(diff.CreatedAt) {
		t.Errorf("el primer incr debería partir del diff (%v) y parte de %v", diff.CreatedAt, incr1.Since)
	}
	if incr2.Kind != "incr" || !incr2.Since.Equal(incr1.CreatedAt) {
		t.Errorf("el segundo incr debería partir del incr anterior (%v) y parte de %v", incr1.CreatedAt, incr2.Since)
	}
	for name, info := range map[string]backupInfo{"diff": diff, "incr1": incr1, "incr2": incr2} {
		if info.Files >= full.Files {
			t.Errorf("%s tiene %d archivos y el full %d: solo debería llevar lo modificado", name, info.Files, full.Files)
		}
	}

	chain := []string{fullPath, diffPath, incr1Path, incr2Path}
	detected, err := AutoDetectBackups(dir)
	if err != nil {
		t.Fatalf("AutoDetectBackups falló: %v", err)
	}
	if !slices.Equal(detected, chain) {
		t.Fatalf("AutoDetectBackups = %v, se esperaba %v", detected, chain)
	}

	// Restore by auto-detection: the state is the one of the last backup (no D).
	restoreAndClean(ctx, t, cl, detected)
	assertRunning(ctx, t, cl, true)
	assertSteps(t, cl, "A", "B", "C", "full")

	// Restore with the explicit list.
	step("E")
	restoreAndClean(ctx, t, cl, chain)
	assertRunning(ctx, t, cl, true)
	assertSteps(t, cl, "A", "B", "C", "full")

	// A differential backup after the incrementals still starts from the full one, so the
	// full plus that differential alone give the whole state.
	step("D")
	diff2Path, diff2 := makeBackup(ctx, t, cl, dir, BackupDifferential)
	if !diff2.Since.Equal(full.CreatedAt) {
		t.Errorf("un diff posterior a los incr debería partir del full (%v) y parte de %v", full.CreatedAt, diff2.Since)
	}
	restoreAndClean(ctx, t, cl, []string{fullPath, diff2Path})
	assertRunning(ctx, t, cl, true)
	assertSteps(t, cl, "A", "B", "C", "D", "full")
}

// 3. Files deleted between backups do not come back.

func TestBackupBorradosEntreBackupsNoResucitan(t *testing.T) {
	cl := newBackupCluster(t, true)
	ctx := backupCtx(t)
	dir := t.TempDir()

	sqlExec(t, cl, "CREATE TABLE t_drop AS SELECT i, repeat('x', 100) AS pad FROM generate_series(1, 5000) AS i")
	sqlExec(t, cl, "CREATE TABLE t_vac AS SELECT i, repeat('y', 100) AS pad FROM generate_series(1, 5000) AS i")
	sqlExec(t, cl, "DELETE FROM t_vac WHERE i > 100")
	dropPath := sqlColumn(t, cl, "SELECT pg_relation_filepath('t_drop')")[0]
	vacOldPath := sqlColumn(t, cl, "SELECT pg_relation_filepath('t_vac')")[0]

	fullPath, _ := makeBackup(ctx, t, cl, dir, BackupFull)

	sqlExec(t, cl, "DROP TABLE t_drop")
	sqlExec(t, cl, "VACUUM FULL t_vac")
	sqlExec(t, cl, "CREATE TABLE marker (x int)")
	sqlExec(t, cl, "INSERT INTO marker VALUES (1)")
	sqlExec(t, cl, "CHECKPOINT") // PostgreSQL deletes the dropped files at the checkpoint
	vacNewPath := sqlColumn(t, cl, "SELECT pg_relation_filepath('t_vac')")[0]
	if vacNewPath == vacOldPath {
		t.Fatalf("VACUUM FULL debería haber cambiado el relfilenode de t_vac (%s)", vacOldPath)
	}
	incrPath, _ := makeBackup(ctx, t, cl, dir, BackupIncremental)

	// The old files were in the full backup and are not in the manifest of the incremental one.
	fullManifest, err := readBackupManifest(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	incrManifest, err := readBackupManifest(incrPath)
	if err != nil {
		t.Fatal(err)
	}
	inManifest := func(m []manifestEntry, path string) bool {
		return slices.ContainsFunc(m, func(e manifestEntry) bool { return e.Path == path })
	}
	for _, p := range []string{dropPath, vacOldPath} {
		if !inManifest(fullManifest, p) {
			t.Fatalf("precondición: %s debería estar en el manifiesto del full", p)
		}
		if inManifest(incrManifest, p) {
			t.Fatalf("precondición: %s no debería estar en el manifiesto del incremental", p)
		}
	}
	if !inManifest(incrManifest, vacNewPath) {
		t.Fatalf("el manifiesto del incremental debería incluir %s", vacNewPath)
	}

	// Without the manifest, extracting the chain would resurrect the old files...
	raw := t.TempDir()
	if err := extractBackup(fullPath, raw, true); err != nil {
		t.Fatal(err)
	}
	if err := extractBackup(incrPath, raw, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(raw, "pg", filepath.FromSlash(dropPath))); err != nil {
		t.Fatalf("precondición: sin manifiesto el archivo borrado debería reaparecer: %v", err)
	}

	// ...and with it the staged cluster is exactly what the manifest lists.
	staging := t.TempDir()
	if err := stageBackups([]string{fullPath, incrPath}, staging); err != nil {
		t.Fatalf("stageBackups falló: %v", err)
	}
	_, scanned, err := collectChangedFiles(filepath.Join(staging, backupPGEntry), time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := renderManifest(scanned), renderManifest(incrManifest); got != want {
		t.Errorf("el clúster extraído no coincide con el manifiesto del incremental:\nextraído:\n%.2000s\nmanifiesto:\n%.2000s", got, want)
	}

	// The real restore.
	restoreAndClean(ctx, t, cl, []string{fullPath, incrPath})
	assertRunning(ctx, t, cl, true)
	pgDir := cl.cfg.PGDataDir()
	for _, p := range []string{dropPath, vacOldPath} {
		if _, err := os.Stat(filepath.Join(pgDir, filepath.FromSlash(p))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s no debería existir tras la restauración (err = %v)", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(pgDir, filepath.FromSlash(vacNewPath))); err != nil {
		t.Errorf("%s debería existir tras la restauración: %v", vacNewPath, err)
	}
	if got := sqlColumn(t, cl, "SELECT count(*)::text FROM t_vac"); !slices.Equal(got, []string{"100"}) {
		t.Errorf("t_vac debería tener 100 filas: %v", got)
	}
	if got := sqlColumn(t, cl, "SELECT (to_regclass('t_drop') IS NOT NULL)::text"); !slices.Equal(got, []string{"false"}) {
		t.Errorf("t_drop debería haber desaparecido: %v", got)
	}
	if got := sqlColumn(t, cl, "SELECT x::text FROM marker"); !slices.Equal(got, []string{"1"}) {
		t.Errorf("marker = %v, se esperaba [1]", got)
	}
}

// archiveModTimes returns the modification time stored in the tar header of every entry of the
// backup at path, by entry name.
func archiveModTimes(t *testing.T, path string) map[string]time.Time {
	t.Helper()
	tr, closeFn, err := openBackup(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	out := map[string]time.Time{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = hdr.ModTime
	}
}

// Recycled WAL segments are renamed (they keep an old mtime) and must still reach the chain, or
// the manifest check of the restore would abort.

func TestBackupIncrementalConWALReciclado(t *testing.T) {
	cl := newBackupCluster(t, true)
	ctx := backupCtx(t)
	dir := t.TempDir()

	sqlExec(t, cl, "CREATE TABLE chain (step text)")
	sqlExec(t, cl, "CREATE TABLE wal_fill (pad text)")
	sqlExec(t, cl, "INSERT INTO chain VALUES ('full')")
	fullPath, full := makeBackup(ctx, t, cl, dir, BackupFull)
	fullManifest, err := readBackupManifest(fullPath)
	if err != nil {
		t.Fatal(err)
	}

	// Several WAL switches and checkpoints, with writes in between, so that PostgreSQL recycles
	// old segments by renaming them to future names.
	for i := 0; i < 6; i++ {
		sqlExec(t, cl, "INSERT INTO wal_fill SELECT repeat('x', 200) FROM generate_series(1, 5000)")
		sqlExec(t, cl, "SELECT pg_switch_wal()")
		sqlExec(t, cl, "CHECKPOINT")
	}
	sqlExec(t, cl, "INSERT INTO chain VALUES ('incr')")
	incrPath, _ := makeBackup(ctx, t, cl, dir, BackupIncremental)

	// Did the scenario happen? Look in the incremental for WAL segments that were not in the
	// full manifest and whose mtime is before the cut (the mtime rule alone would have missed them).
	inFull := map[string]bool{}
	for _, e := range fullManifest {
		if !e.Dir {
			inFull[e.Path] = true
		}
	}
	cut := full.CreatedAt.Add(-backupSinceMargin)
	recycled := 0
	for name, mod := range archiveModTimes(t, incrPath) {
		rel := strings.TrimPrefix(name, backupPGEntry+"/")
		segment, isWAL := strings.CutPrefix(rel, "pg_wal/")
		if isWAL && len(segment) == 24 && !inFull[rel] && !mod.After(cut) {
			recycled++
		}
	}
	if recycled == 0 {
		t.Log("AVISO: no apareció ningún segmento de WAL reciclado (ruta nueva y mtime anterior al corte): este test no garantiza el escenario")
	} else {
		t.Logf("segmentos de WAL con ruta nueva y mtime anterior al corte incluidos en el incremental: %d", recycled)
	}

	restoreAndClean(ctx, t, cl, []string{fullPath, incrPath})
	assertRunning(ctx, t, cl, true)
	assertSteps(t, cl, "full", "incr")
	if got := sqlColumn(t, cl, "SELECT count(*)::text FROM wal_fill"); !slices.Equal(got, []string{"30000"}) {
		t.Errorf("wal_fill = %v, se esperaban 30000 filas", got)
	}
}

// 4. Invalid chains (no PostgreSQL needed: the checks are made before touching anything).

func TestRestoreCadenasInvalidas(t *testing.T) {
	ctx := backupCtx(t)
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	dir := t.TempDir()
	other := t.TempDir()

	full := writeFakeBackup(t, dir, "full", t0, time.Time{})
	diff := writeFakeBackup(t, dir, "diff", t0.Add(1*time.Minute), t0)
	incr1 := writeFakeBackup(t, dir, "incr", t0.Add(2*time.Minute), t0.Add(1*time.Minute))
	incr2 := writeFakeBackup(t, dir, "incr", t0.Add(3*time.Minute), t0.Add(2*time.Minute))
	fullB := writeFakeBackup(t, other, "full", t0.Add(10*time.Minute), time.Time{})
	diffEarly := writeFakeBackup(t, other, "diff", t0.Add(30*time.Second), t0)

	infos, err := validateRestoreChain([]string{full, diff, incr1, incr2})
	if err != nil {
		t.Fatalf("una cadena válida se rechaza: %v", err)
	}
	if len(infos) != 4 {
		t.Errorf("validateRestoreChain devolvió %d infos, se esperaban 4", len(infos))
	}
	if _, err := validateRestoreChain([]string{full}); err != nil {
		t.Errorf("un full solo es una cadena válida: %v", err)
	}
	if _, err := validateRestoreChain([]string{full, diffEarly}); err != nil {
		t.Errorf("full y un diff suyo es una cadena válida: %v", err)
	}

	type chainCase struct {
		name  string
		files []string
		want  string
	}
	cases := []chainCase{
		{"falta un eslabón intermedio", []string{full, diff, incr2}, "falta un archivo en la cadena"},
		{"falta el diff del que parte el incremental", []string{full, incr1}, "falta un archivo en la cadena"},
		{"el primero no es un full", []string{diff, incr1}, "el primer archivo debe ser un backup full"},
		{"solo hay incrementales", []string{incr1, incr2}, "el primer archivo debe ser un backup full"},
		{"orden incorrecto", []string{full, diff, diffEarly}, "orden incorrecto"},
		{"orden invertido", []string{full, incr2, incr1}, "orden incorrecto"},
		{"la referencia aparece más adelante", []string{full, incr1, diff}, "aparece más adelante en la lista"},
		{"un segundo full", []string{full, fullB}, "solo el primer archivo puede serlo"},
		{"no es un backup", []string{filepath.Join(dir, "otro.tar.zst")}, "no parece un backup de lodan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateRestoreChain(tc.files)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("validateRestoreChain: error = %v, se esperaba que contuviera %q", err, tc.want)
			}
			// Restore validates the chain before touching the cluster (here there is none).
			_, err = (&Cluster{}).Restore(ctx, tc.files)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Restore: error = %v, se esperaba que contuviera %q", err, tc.want)
			}
		})
	}
	if _, err := (&Cluster{}).Restore(ctx, nil); err == nil || !strings.Contains(err.Error(), "ningún backup") {
		t.Errorf("Restore sin archivos: error = %v", err)
	}

	t.Run("AutoDetectBackups ordena la cadena", func(t *testing.T) {
		got, err := AutoDetectBackups(dir)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{full, diff, incr1, incr2}; !slices.Equal(got, want) {
			t.Errorf("AutoDetectBackups = %v, se esperaba %v", got, want)
		}
		if _, err := validateRestoreChain(got); err != nil {
			t.Errorf("lo que detecta AutoDetectBackups no es una cadena válida: %v", err)
		}
	})

	t.Run("AutoDetectBackups elige el último full", func(t *testing.T) {
		d := t.TempDir()
		writeFakeBackup(t, d, "full", t0, time.Time{})
		writeFakeBackup(t, d, "incr", t0.Add(1*time.Minute), t0)
		f2 := writeFakeBackup(t, d, "full", t0.Add(2*time.Minute), time.Time{})
		i2 := writeFakeBackup(t, d, "incr", t0.Add(3*time.Minute), t0.Add(2*time.Minute))
		if err := os.WriteFile(filepath.Join(d, "notas.txt"), []byte("no es un backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := AutoDetectBackups(d)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{f2, i2}; !slices.Equal(got, want) {
			t.Errorf("AutoDetectBackups = %v, se esperaba %v", got, want)
		}
	})

	t.Run("AutoDetectBackups sin full", func(t *testing.T) {
		empty := t.TempDir()
		onlyIncr := t.TempDir()
		writeFakeBackup(t, onlyIncr, "incr", t0.Add(1*time.Minute), t0)
		for name, d := range map[string]string{"vacío": empty, "solo incr": onlyIncr} {
			_, err := AutoDetectBackups(d)
			if err == nil || !strings.Contains(err.Error(), "no hay ningún backup full") {
				t.Errorf("%s: error = %v, se esperaba que no hay ningún backup full", name, err)
			}
		}
		_, err := AutoDetectBackups(filepath.Join(empty, "no-existe"))
		if err == nil || !strings.Contains(err.Error(), "no existe") {
			t.Errorf("directorio inexistente: error = %v", err)
		}
	})
}

// 5. Inconsistent manifest.

func TestApplyManifest(t *testing.T) {
	baseManifest := []manifestEntry{
		{"PG_VERSION", false, 3},
		{"base", true, 0},
		{"base/1", true, 0},
		{"base/1/100", false, 10},
	}
	// stagedTree builds an extracted cluster that matches baseManifest.
	stagedTree := func(t *testing.T) string {
		t.Helper()
		pg := t.TempDir()
		writeTree(t, pg, []string{"base/1"}, map[string]string{"PG_VERSION": "18\n", "base/1/100": "0123456789"})
		return pg
	}

	t.Run("borra lo que el manifiesto no lista", func(t *testing.T) {
		pg := stagedTree(t)
		writeTree(t, pg, []string{"stale/sub", "base/vacio"}, map[string]string{"base/1/extra": "12345", "stale/sub/x": "x", "suelto": "y"})
		if err := applyManifest(pg, baseManifest); err != nil {
			t.Fatalf("applyManifest falló: %v", err)
		}
		for _, p := range []string{"base/1/extra", "stale", "base/vacio", "suelto"} {
			if _, err := os.Lstat(filepath.Join(pg, filepath.FromSlash(p))); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s debería haberse borrado (err = %v)", p, err)
			}
		}
		for _, e := range baseManifest {
			if _, err := os.Lstat(filepath.Join(pg, filepath.FromSlash(e.Path))); err != nil {
				t.Errorf("%s debería seguir ahí: %v", e.Path, err)
			}
		}
	})

	manyMissing := append([]manifestEntry{}, baseManifest...)
	for _, name := range []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8"} {
		manyMissing = append(manyMissing, manifestEntry{name, false, 1})
	}
	type applyCase struct {
		name     string
		manifest []manifestEntry
		want     string
	}
	cases := []applyCase{
		{"falta un archivo", append(append([]manifestEntry{}, baseManifest...), manifestEntry{"zzz", false, 4}), "falta zzz"},
		{"falta un directorio", append(append([]manifestEntry{}, baseManifest...), manifestEntry{"zzz", true, 0}), "falta zzz"},
		{"el tamaño no cuadra", []manifestEntry{{"PG_VERSION", false, 3}, {"base", true, 0}, {"base/1", true, 0}, {"base/1/100", false, 11}}, "base/1/100 mide 10 bytes"},
		{"un archivo que debería ser directorio", []manifestEntry{{"PG_VERSION", true, 0}}, "PG_VERSION debería ser un directorio"},
		{"un directorio que debería ser archivo", []manifestEntry{{"PG_VERSION", false, 3}, {"base", false, 0}}, "base debería ser un archivo"},
		{"muchos problemas", manyMissing, "y 3 más"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := applyManifest(stagedTree(t), tc.manifest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, se esperaba que contuviera %q", err, tc.want)
			}
		})
	}
}

func TestRestoreManifiestoInconsistenteNoTocaElClusterActual(t *testing.T) {
	cl := newBackupCluster(t, true)
	ctx := backupCtx(t)
	dir := t.TempDir()
	badDir := t.TempDir()

	sqlExec(t, cl, "CREATE TABLE chain (step text)")
	sqlExec(t, cl, "INSERT INTO chain VALUES ('full')")
	path, _ := makeBackup(ctx, t, cl, dir, BackupFull)

	// Same backup, but its manifest lists a file that is not in the archive.
	bad := filepath.Join(badDir, filepath.Base(path))
	rewriteManifest(t, path, bad, func(manifest string) string { return manifest + "f 10 zzz-falta.dat\n" })

	sqlExec(t, cl, "INSERT INTO chain VALUES ('despues')")
	previous, err := cl.Restore(ctx, []string{bad})
	if err == nil {
		t.Fatal("Restore debería fallar con un manifiesto que lista un archivo inexistente")
	}
	if previous != "" {
		t.Errorf("Restore devolvió %q como clúster anterior, pero no debería haber tocado nada", previous)
	}
	for _, want := range []string{"zzz-falta.dat", "manifiesto"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("el error debería mencionar %q: %v", want, err)
		}
	}

	// The current cluster is intact: running, with the data it had before the failed restore.
	assertRunning(ctx, t, cl, true)
	assertSteps(t, cl, "despues", "full")
	for _, pattern := range []string{".restore-*", "pg.pre-restore-*"} {
		left, err := filepath.Glob(filepath.Join(cl.cfg.DataDir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 0 {
			t.Errorf("quedaron restos de la restauración fallida: %v", left)
		}
	}

	// A backup without manifest.txt is rejected as well.
	noManifest := filepath.Join(badDir, "lodan_backup_2026-01-02_030405.full.tar.zst")
	info := backupInfo{Kind: "full", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), PGVersion: "18"}
	writeRawBackup(t, noManifest, rawEntry{backupInfoFile, info.render()})
	if _, err := cl.Restore(ctx, []string{noManifest}); err == nil || !strings.Contains(err.Error(), backupManifestFile) {
		t.Errorf("un backup sin %s debería rechazarse: %v", backupManifestFile, err)
	}
	assertRunning(ctx, t, cl, true)
	assertSteps(t, cl, "despues", "full")
}

// 6. Rejected destinations and missing full backups.

func TestBackupRechazos(t *testing.T) {
	cl := newBackupCluster(t, false)
	ctx := backupCtx(t)
	pgDir := cl.cfg.PGDataDir()

	t.Run("destino dentro de pg", func(t *testing.T) {
		inside := filepath.Join(pgDir, "backups", "x")
		for _, dest := range []string{pgDir, filepath.Join(pgDir, "base"), inside} {
			_, err := cl.Backup(ctx, dest, BackupFull)
			if err == nil || !strings.Contains(err.Error(), "dentro del directorio de datos") {
				t.Errorf("destino %s: error = %v", dest, err)
			}
		}
		if _, err := os.Lstat(filepath.Join(pgDir, "backups")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("el destino rechazado no debe dejar directorios dentro de pg/ (err = %v)", err)
		}
	})

	t.Run("destino dentro de pg a través de un enlace", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "enlace")
		if err := os.Symlink(pgDir, link); err != nil {
			t.Skipf("no se pueden crear enlaces simbólicos: %v", err)
		}
		_, err := cl.Backup(ctx, filepath.Join(link, "nuevo"), BackupFull)
		if err == nil || !strings.Contains(err.Error(), "dentro del directorio de datos") {
			t.Errorf("error = %v", err)
		}
	})

	t.Run("incremental y diferencial sin full", func(t *testing.T) {
		empty := t.TempDir()
		onlyIncr := t.TempDir()
		t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		writeFakeBackup(t, onlyIncr, "incr", t0.Add(time.Minute), t0)
		for _, dest := range []string{empty, onlyIncr, filepath.Join(t.TempDir(), "nuevo")} {
			for _, typ := range []string{BackupIncremental, BackupDifferential} {
				_, err := cl.Backup(ctx, dest, typ)
				if err == nil || !strings.Contains(err.Error(), "no hay ningún backup full") {
					t.Errorf("%s en %s: error = %v", typ, dest, err)
				}
			}
		}
	})

	t.Run("tipo desconocido", func(t *testing.T) {
		_, err := cl.Backup(ctx, t.TempDir(), "mensual")
		if err == nil || !strings.Contains(err.Error(), "tipo de backup desconocido") {
			t.Errorf("error = %v", err)
		}
	})
}

// 7. Backup with PostgreSQL stopped and restore with PostgreSQL running.

func TestBackupConPostgreSQLParadoYRestoreConPostgreSQLEnMarcha(t *testing.T) {
	cl := newBackupCluster(t, true)
	ctx := backupCtx(t)
	dir := t.TempDir()

	sqlExec(t, cl, "CREATE TABLE chain (step text)")
	sqlExec(t, cl, "INSERT INTO chain VALUES ('original')")
	if err := cl.Stop(ctx); err != nil {
		t.Fatalf("Stop falló: %v", err)
	}
	assertRunning(ctx, t, cl, false)

	path, err := cl.Backup(ctx, dir, BackupFull)
	if err != nil {
		t.Fatalf("Backup con PostgreSQL parado falló: %v", err)
	}
	assertRunning(ctx, t, cl, false) // it was stopped: it must stay stopped

	if err := cl.Start(ctx); err != nil {
		t.Fatalf("Start falló: %v", err)
	}
	sqlExec(t, cl, "INSERT INTO chain VALUES ('despues')")
	assertRunning(ctx, t, cl, true)

	previous, err := cl.Restore(ctx, []string{path})
	if err != nil {
		t.Fatalf("Restore con PostgreSQL en marcha falló: %v", err)
	}
	// PostgreSQL was stopped before the cluster was set aside (a running one leaves postmaster.pid).
	if _, err := os.Stat(filepath.Join(previous, "postmaster.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("el clúster anterior no debería tener postmaster.pid: se paró antes de apartarlo (err = %v)", err)
	}
	assertRunning(ctx, t, cl, true) // and it is running again afterwards
	assertSteps(t, cl, "original")
}

// 8. Format 1 is rejected.

func TestBackupFormato1Rechazado(t *testing.T) {
	ctx := backupCtx(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "lodan_backup_2026-01-02_030405.full.tar.zst")
	info1 := "formato: lodan-backup/1\ntipo: full\nfecha: 2026-01-02T03:04:05Z\ntamano_original_bytes: 0\narchivos: 0\npostgresql: 18\n"
	writeRawBackup(t, path, rawEntry{backupInfoFile, info1})

	check := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "formato") || !strings.Contains(err.Error(), "lodan-backup/1") {
			t.Errorf("%s: error = %v, se esperaba un rechazo del formato lodan-backup/1", what, err)
		}
	}
	_, err := readBackupInfo(path)
	check("readBackupInfo", err)
	_, err = validateRestoreChain([]string{path})
	check("validateRestoreChain", err)
	_, err = AutoDetectBackups(dir)
	check("AutoDetectBackups", err)
	_, err = (&Cluster{}).Restore(ctx, []string{path})
	check("Restore", err)

	// Control: the same archive in the current format is accepted.
	ok := filepath.Join(t.TempDir(), "lodan_backup_2026-01-02_030405.full.tar.zst")
	writeRawBackup(t, ok, rawEntry{backupInfoFile, strings.Replace(info1, "lodan-backup/1", backupFormat, 1)})
	if _, err := readBackupInfo(ok); err != nil {
		t.Errorf("el formato actual (%s) debería aceptarse: %v", backupFormat, err)
	}
}

// 9. Staging copy: no leftovers, orphans cleaned, failures cleaned up.

// backupLeftovers returns the names in dir that a finished backup must never leave behind:
// staging directories and .part files.
func backupLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), backupStagingSuffix) || strings.HasSuffix(e.Name(), ".part") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestBackupNoDejaStagingNiPart(t *testing.T) {
	cl := newBackupCluster(t, true)
	ctx := backupCtx(t)
	dir := t.TempDir()

	sqlExec(t, cl, "CREATE TABLE chain (step text)")
	sqlExec(t, cl, "INSERT INTO chain VALUES ('full')")
	makeBackup(ctx, t, cl, dir, BackupFull)
	sqlExec(t, cl, "INSERT INTO chain VALUES ('incr')")
	makeBackup(ctx, t, cl, dir, BackupIncremental)

	if left := backupLeftovers(t, dir); len(left) != 0 {
		t.Errorf("tras backups correctos quedaron restos en el destino: %v", left)
	}
	list, err := listBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("el destino debería tener 2 backups y tiene %d", len(list))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("el destino solo debería contener los 2 archivos de backup y tiene %d entradas", len(entries))
	}
}

func TestBackupLimpiaStagingHuerfano(t *testing.T) {
	cl := newBackupCluster(t, false)
	ctx := backupCtx(t)
	dir := t.TempDir()

	// Leftovers of interrupted backups, and a directory that is not one of them.
	orphans := []string{
		".lodan_backup_2026-01-02_030405.full.staging",
		".lodan_backup_2026-01-02_030405.incr.staging",
	}
	for _, name := range orphans {
		writeTree(t, filepath.Join(dir, name), []string{"pg/base"}, map[string]string{"pg/PG_VERSION": "18\n", "secret": "x"})
	}
	other := filepath.Join(dir, ".lodan_backup_notas")
	writeTree(t, other, nil, map[string]string{"a.txt": "no es de lodan"})

	// A staging directory is never listed as a backup.
	list, err := listBackups(dir)
	if err != nil {
		t.Fatalf("listBackups falló con staging en el destino: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("listBackups no debe considerar un staging como backup: %v", list)
	}
	if _, err := AutoDetectBackups(dir); err == nil || !strings.Contains(err.Error(), "no hay ningún backup full") {
		t.Fatalf("AutoDetectBackups con solo staging: error = %v", err)
	}

	path, err := cl.Backup(ctx, dir, BackupFull)
	if err != nil {
		t.Fatalf("Backup falló: %v", err)
	}
	for _, name := range orphans {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("el staging huérfano %s debería haberse borrado (err = %v)", name, err)
		}
	}
	if _, err := os.Lstat(other); err != nil {
		t.Errorf("un directorio que no es un staging no debe borrarse: %v", err)
	}
	if left := backupLeftovers(t, dir); len(left) != 0 {
		t.Errorf("quedaron restos en el destino: %v", left)
	}
	if _, err := readBackupInfo(path); err != nil {
		t.Errorf("el backup creado no es legible: %v", err)
	}
	assertRunning(ctx, t, cl, false) // it was stopped: it must stay stopped
}

// With the lock of the destination held by another backup, a backup fails (after the wait) saying
// so, and touches neither the other backup's staging directory nor PostgreSQL. Once the lock is
// released the same backup works and cleans up the staging as an orphan.
func TestBackupConLockDelDestinoOcupado(t *testing.T) {
	cl := newBackupCluster(t, false)
	ctx := backupCtx(t)
	dir := t.TempDir()

	old := backupLockWait
	backupLockWait = time.Second
	t.Cleanup(func() { backupLockWait = old })

	// Another backup, taken by hand: lock plus its staging directory.
	unlock, err := acquireLock(ctx, filepath.Join(dir, backupLockName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	foreign := filepath.Join(dir, ".lodan_backup_2026-01-02_030405.full.staging")
	writeTree(t, foreign, []string{"pg"}, map[string]string{"pg/PG_VERSION": "18\n"})

	// The lock file is not a backup.
	if list, err := listBackups(dir); err != nil || len(list) != 0 {
		t.Errorf("listBackups con el lock del destino: %v, %v", list, err)
	}
	if _, err := AutoDetectBackups(dir); err == nil || !strings.Contains(err.Error(), "no hay ningún backup full") {
		t.Errorf("AutoDetectBackups con el lock del destino: error = %v", err)
	}

	path, err := cl.Backup(ctx, dir, BackupFull)
	if err == nil || !strings.Contains(err.Error(), "otro backup en curso") {
		t.Fatalf("Backup con el destino bloqueado: error = %v, se esperaba que mencionara otro backup en curso", err)
	}
	if path != "" {
		t.Errorf("Backup devolvió la ruta %q pese a fallar", path)
	}
	if _, err := os.Stat(filepath.Join(foreign, "pg", "PG_VERSION")); err != nil {
		t.Errorf("el staging del otro backup no debe tocarse: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, backupLockName)); err != nil {
		t.Errorf("el lock del otro backup no debe borrarse: %v", err)
	}

	// Released: the backup goes through and the staging, now an orphan, is cleaned.
	unlock()
	if _, err := cl.Backup(ctx, dir, BackupFull); err != nil {
		t.Fatalf("Backup con el destino libre falló: %v", err)
	}
	if _, err := os.Lstat(foreign); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("el staging huérfano debería haberse borrado (err = %v)", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, backupLockName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("el backup debe liberar el lock del destino (err = %v)", err)
	}
}

// A failure while the data is copied (here a secret that cannot be read) leaves nothing behind
// and PostgreSQL running again.
func TestBackupFalloEnLaCopiaNoDejaRestos(t *testing.T) {
	if os.Geteuid() <= 0 {
		t.Skip("con root (o sin soporte de uid) los permisos no impiden leer el secreto")
	}
	cl := newBackupCluster(t, true)
	ctx := backupCtx(t)
	dir := t.TempDir()
	secret := cl.cfg.SecretFile()

	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	restored := false
	restoreSecret := func() {
		if !restored {
			restored = true
			if err := os.Chmod(secret, 0o600); err != nil {
				t.Errorf("no se pudo devolver el permiso del secreto: %v", err)
			}
		}
	}
	t.Cleanup(restoreSecret)

	path, err := cl.Backup(ctx, dir, BackupFull)
	restoreSecret()
	if err == nil {
		t.Fatal("Backup debería fallar si no se puede leer el secreto")
	}
	if path != "" {
		t.Errorf("Backup devolvió la ruta %q pese a fallar", path)
	}
	assertRunning(ctx, t, cl, true)
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("un backup fallido no debe dejar nada en el destino: %v", names)
	}

	// With the secret readable again the same destination works.
	makeBackup(ctx, t, cl, dir, BackupFull)
	if left := backupLeftovers(t, dir); len(left) != 0 {
		t.Errorf("quedaron restos en el destino: %v", left)
	}
}

// A failure while compressing (here a staged file that has disappeared) removes the .part file.
// There is no hook to fail between the copy and the compression of a real Backup, so this tests
// writeBackupArchive, which is what Backup runs in that phase.
func TestWriteBackupArchiveFalloNoDejaPart(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone")
	writeTree(t, dir, nil, map[string]string{"gone": "contenido"})
	fi, err := os.Lstat(gone)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, backupPrefix+"2026-01-02_030405.full"+backupExt)
	info := backupInfo{Kind: "full", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), PGVersion: "18"}
	err = writeBackupArchive(dest, info, renderManifest(nil), []backupEntry{{Path: gone, Name: "pg/gone", Info: fi}})
	if err == nil {
		t.Fatal("writeBackupArchive debería fallar si falta un archivo de la copia")
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 0 {
		t.Errorf("tras fallar no debe quedar ni el destino ni su .part: %v", entries)
	}
}

func TestCopyToStaging(t *testing.T) {
	ctx := backupCtx(t)
	root := t.TempDir()
	writeTree(t, root, []string{"pg/base"}, map[string]string{"pg/a": "hola", "pg/base/b": "0123456789"})
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chmod(filepath.Join(root, "pg", "a"), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pg/a", "pg/base/b"} {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(name)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	entries, _, err := collectChangedFiles(filepath.Join(root, "pg"), time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	staging := t.TempDir()
	staged, err := copyToStaging(ctx, staging, entries)
	if err != nil {
		t.Fatalf("copyToStaging falló: %v", err)
	}
	if len(staged) != len(entries) {
		t.Fatalf("copyToStaging devolvió %d entradas y se esperaban %d", len(staged), len(entries))
	}
	for _, e := range staged {
		if !strings.HasPrefix(e.Path, staging) {
			t.Errorf("la entrada %s debe apuntar dentro del staging: %s", e.Name, e.Path)
		}
	}
	got, err := os.Stat(filepath.Join(staging, "pg", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode().Perm() != 0o640 || !got.ModTime().Equal(old) || got.Size() != 4 {
		t.Errorf("la copia de pg/a no conserva modo, fecha y tamaño: %v %v %d", got.Mode().Perm(), got.ModTime(), got.Size())
	}
	if data, err := os.ReadFile(filepath.Join(staging, "pg", "base", "b")); err != nil || string(data) != "0123456789" {
		t.Errorf("contenido de pg/base/b = %q (err = %v)", data, err)
	}

	// A file that changed size after it was listed is detected.
	if err := os.WriteFile(filepath.Join(root, "pg", "a"), []byte("hola mundo"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := copyToStaging(ctx, t.TempDir(), entries); err == nil || !strings.Contains(err.Error(), "cambió de tamaño") {
		t.Errorf("un archivo que cambia de tamaño debería detectarse: %v", err)
	}
}

// Manifest file format and the walk that produces it (no PostgreSQL needed).

func TestManifestFormatoYRecorrido(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, []string{"base/1", "pg_wal"}, map[string]string{
		"PG_VERSION":        "18\n",
		"base/1/100":        "0123456789",
		"base/1/101":        "abc",
		"base/1/with space": "x",
		"postmaster.pid":    "1234\n",
	})
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := old.Add(24 * time.Hour)
	for _, name := range []string{"PG_VERSION", "base/1/100", "base/1/with space"} {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(name)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"base/1/101", "postmaster.pid"} {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(name)), recent, recent); err != nil {
			t.Fatal(err)
		}
	}
	names := func(entries []backupEntry) []string {
		var out []string
		for _, e := range entries {
			out = append(out, e.Name)
		}
		slices.Sort(out)
		return out
	}

	// Only what changed after the cut is copied, but the manifest lists everything.
	reference := map[string]int64{"PG_VERSION": 3, "base/1/100": 10, "base/1/with space": 1}
	entries, manifest, err := collectChangedFiles(root, old.Add(time.Hour), reference)
	if err != nil {
		t.Fatal(err)
	}
	wantText := "f 3 PG_VERSION\n" +
		"d 0 base\n" +
		"d 0 base/1\n" +
		"f 10 base/1/100\n" +
		"f 3 base/1/101\n" +
		"f 1 base/1/with space\n" +
		"d 0 pg_wal\n"
	if got := renderManifest(manifest); got != wantText {
		t.Errorf("manifiesto:\n%s\nse esperaba:\n%s", got, wantText)
	}
	wantNames := []string{"pg", "pg/base", "pg/base/1", "pg/base/1/101", "pg/pg_wal"}
	if got := names(entries); !slices.Equal(got, wantNames) {
		t.Errorf("entradas a copiar = %v, se esperaba %v", got, wantNames)
	}

	// A zero cut copies every file (never postmaster.pid) and the manifest is the same.
	entries, manifest2, err := collectChangedFiles(root, time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if renderManifest(manifest2) != wantText {
		t.Error("el manifiesto no debería depender de la fecha de corte")
	}
	wantNames = []string{"pg", "pg/PG_VERSION", "pg/base", "pg/base/1", "pg/base/1/100", "pg/base/1/101", "pg/base/1/with space", "pg/pg_wal"}
	if got := names(entries); !slices.Equal(got, wantNames) {
		t.Errorf("entradas de un full = %v, se esperaba %v", got, wantNames)
	}

	// The text parses back to the same entries.
	parsed, err := parseManifest(strings.NewReader(wantText))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(parsed, manifest) {
		t.Errorf("parseManifest = %v, se esperaba %v", parsed, manifest)
	}

	// Symbolic links (tablespaces) are rejected.
	if err := os.Symlink(filepath.Join(root, "PG_VERSION"), filepath.Join(root, "enlace")); err != nil {
		t.Logf("no se pueden crear enlaces simbólicos, se omite esa comprobación: %v", err)
		return
	}
	if _, _, err := collectChangedFiles(root, time.Time{}, nil); err == nil || !strings.Contains(err.Error(), "enlace simbólico") {
		t.Errorf("un enlace simbólico debería rechazarse: %v", err)
	}
}

// Files that rename(2) or a rewrite changed without a recent modification time are still copied
// when the reference manifest does not list them (or lists them with another size).
func TestCollectChangedFilesUsaElManifiestoDeReferencia(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{"base/1/keep": "aaaa", "base/1/grown": "bbbbbb", "base/1/touched": "cc", "pg_wal/000000010000000000000009": "recycled segment"}
	writeTree(t, root, []string{"base/1", "pg_wal"}, files)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := old.Add(24 * time.Hour)
	for _, name := range []string{"base/1/keep", "base/1/grown", "pg_wal/000000010000000000000009"} {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(name)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(root, "base", "1", "touched"), recent, recent); err != nil {
		t.Fatal(err)
	}

	// The reference knows keep (same size), grown (smaller than now) and touched (same size);
	// it does not know the recycled segment, which was renamed here keeping its old mtime.
	reference := map[string]int64{"base/1/keep": 4, "base/1/grown": 3, "base/1/touched": 2}
	entries, _, err := collectChangedFiles(root, old.Add(time.Hour), reference)
	if err != nil {
		t.Fatal(err)
	}
	var copied []string
	for _, e := range entries {
		if e.Info.Mode().IsRegular() {
			copied = append(copied, e.Name)
		}
	}
	slices.Sort(copied)
	want := []string{"pg/base/1/grown", "pg/base/1/touched", "pg/pg_wal/000000010000000000000009"}
	if !slices.Equal(copied, want) {
		t.Errorf("archivos copiados = %v, se esperaba %v (keep tiene mtime antiguo, misma ruta y mismo tamaño: no se copia)", copied, want)
	}
}

func TestParseManifestRechazaTextosInvalidos(t *testing.T) {
	type badCase struct {
		text string
		want string
	}
	cases := []badCase{
		{"", "vacío"},
		{"f 3 PG_VERSION", "truncado"},
		{"x 3 a\n", "tipo desconocido"},
		{"f tres a\n", "tamaño inválido"},
		{"f -1 a\n", "tamaño inválido"},
		{"d 5 a\n", "un directorio no puede tener tamaño"},
		{"f 3 ../a\n", "ruta inválida"},
		{"f 3 /a\n", "ruta inválida"},
		{"f 3 .\n", "ruta inválida"},
		{"f 3 a//b\n", "ruta inválida"},
		{"f 3 a/\n", "ruta inválida"},
		{"f 3 \n", "ruta inválida"},
		{"f 3 b\nf 3 a\n", "fuera de orden"},
		{"f 3 a\nf 3 a\n", "repetida"},
		{"f3a\n", "formato inválido"},
		{"f 3\n", "formato inválido"},
	}
	for _, tc := range cases {
		_, err := parseManifest(strings.NewReader(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseManifest(%q): error = %v, se esperaba que contuviera %q", tc.text, err, tc.want)
		}
	}

	// Over the read limit.
	_, err := parseManifest(strings.NewReader(strings.Repeat("x", manifestMaxBytes+1)))
	if err == nil || !strings.Contains(err.Error(), "supera el límite") {
		t.Errorf("un manifiesto por encima del límite debería rechazarse: %v", err)
	}
}
