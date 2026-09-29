package database

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// show returns the value of a PostgreSQL setting through SHOW.
func show(ctx context.Context, t *testing.T, conn *pgx.Conn, name string) string {
	t.Helper()
	var v string
	if err := conn.QueryRow(ctx, "SHOW "+name).Scan(&v); err != nil {
		t.Fatalf("SHOW %s falló: %v", name, err)
	}
	return v
}

func TestCicloDeVidaYSeguridad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	cl, cfg := newLocalCluster(t, 4)
	if cl.Initialized() {
		t.Fatal("el clúster no debería estar inicializado todavía")
	}

	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init falló: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testTimeout)
		defer stopCancel()
		_ = cl.Stop(stopCtx)
	})
	if !cl.Initialized() {
		t.Fatal("Initialized debería ser true tras Init")
	}
	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init no es idempotente: %v", err)
	}

	// pg_hba.conf: una sola regla, sin trust.
	hba, err := os.ReadFile(filepath.Join(cfg.PGDataDir(), "pg_hba.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hba), "trust") {
		t.Errorf("pg_hba.conf no debe contener trust:\n%s", hba)
	}
	if !strings.Contains(string(hba), "host all lodan 127.0.0.1/32 scram-sha-256") {
		t.Errorf("pg_hba.conf no contiene la regla esperada:\n%s", hba)
	}

	// Contraseña en fichero 0600 (Windows no tiene permisos Unix).
	if runtime.GOOS != "windows" {
		info, err := os.Stat(cfg.SecretFile())
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("permisos de secret = %v, se esperaba 0600", info.Mode().Perm())
		}
	}
	// El fichero temporal de contraseña no debe quedar en el directorio de datos.
	leftovers, err := filepath.Glob(filepath.Join(cfg.DataDir, ".pwfile-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("quedaron ficheros temporales de contraseña: %v", leftovers)
	}

	running, err := cl.Status(ctx)
	if err != nil {
		t.Fatalf("Status falló: %v", err)
	}
	if running {
		t.Fatal("el clúster no debería estar en marcha antes de Start")
	}

	if err := cl.Start(ctx); err != nil {
		t.Fatalf("Start falló: %v", err)
	}
	if err := cl.Start(ctx); err != nil {
		t.Fatalf("Start con el clúster en marcha debería ser un no-op: %v", err)
	}
	running, err = cl.Status(ctx)
	if err != nil {
		t.Fatalf("Status falló: %v", err)
	}
	if !running {
		t.Fatal("el clúster debería estar en marcha tras Start")
	}

	// Conexión con la contraseña correcta.
	dsn, err := cl.DSN("postgres")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("la conexión con la contraseña correcta falló: %v", err)
	}
	defer conn.Close(context.Background())

	if got := show(ctx, t, conn, "listen_addresses"); got != "127.0.0.1" {
		t.Errorf("listen_addresses = %q, se esperaba 127.0.0.1", got)
	}
	if got := show(ctx, t, conn, "password_encryption"); got != "scram-sha-256" {
		t.Errorf("password_encryption = %q, se esperaba scram-sha-256", got)
	}
	if got := show(ctx, t, conn, "unix_socket_directories"); got != "" {
		t.Errorf("unix_socket_directories = %q, se esperaba vacío", got)
	}

	// Conexión con una contraseña incorrecta.
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("lodan", "contraseña-incorrecta")
	if bad, err := pgx.Connect(ctx, u.String()); err == nil {
		_ = bad.Close(context.Background())
		t.Error("la conexión con una contraseña incorrecta debería fallar")
	}

	// EnsureDatabase crea la base y es idempotente.
	for i := 0; i < 2; i++ {
		if err := cl.EnsureDatabase(ctx, "otra_base"); err != nil {
			t.Fatalf("EnsureDatabase (intento %d) falló: %v", i+1, err)
		}
	}
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_database WHERE datname = 'otra_base'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("otra_base aparece %d veces en pg_database, se esperaba 1", n)
	}

	if err := cl.Stop(ctx); err != nil {
		t.Fatalf("Stop falló: %v", err)
	}
	running, err = cl.Status(ctx)
	if err != nil {
		t.Fatalf("Status falló: %v", err)
	}
	if running {
		t.Error("el clúster debería estar parado tras Stop")
	}
	if err := cl.Stop(ctx); err != nil {
		t.Errorf("Stop con el clúster parado debería ser un no-op: %v", err)
	}
}

func TestEnsureDatabaseRechazaNombreInvalido(t *testing.T) {
	cl, _ := newLocalCluster(t, 4)
	invalidos := []string{
		"",
		"Lodan",
		"1lodan",
		"lo-dan",
		"lo dan",
		`lodan"`,
		"lodan; DROP DATABASE postgres",
	}
	for _, name := range invalidos {
		err := cl.EnsureDatabase(context.Background(), name)
		if err == nil {
			t.Errorf("EnsureDatabase(%q) debería rechazar el nombre", name)
			continue
		}
		if !strings.Contains(err.Error(), "inválido") {
			t.Errorf("EnsureDatabase(%q): el error no es el de validación: %v", name, err)
		}
	}
}

func TestMigrate(t *testing.T) {
	tc := NewTestCluster(t, 8)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	settings := Settings{Dims: 8, Model: "test-model"}

	// NewTestCluster ya migró una vez: repetir no debe fallar ni duplicar nada.
	if err := Migrate(ctx, tc.Pool, settings); err != nil {
		t.Fatalf("Migrate no es idempotente: %v", err)
	}
	var applied int
	if err := tc.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Errorf("schema_migrations tiene %d filas, se esperaban 2", applied)
	}

	// 0002 sustituye el índice de la ficha del tema por dos parciales.
	for name, want := range map[string]bool{
		"memory_topics_topic_id_kind_ts_idx": false,
		"memory_topics_profile_idx":          true,
		"memory_topics_events_idx":           true,
	} {
		var exists bool
		if err := tc.Pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Errorf("índice %s: existe = %v, se esperaba %v", name, exists, want)
		}
	}

	tablas := []string{
		"schema_migrations", "lodan_settings", "embedding_models", "sessions", "memories",
		"topics", "memory_topics", "session_topics", "memory_relations",
	}
	for _, name := range tablas {
		var exists bool
		if err := tc.Pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("falta la tabla %s", name)
		}
	}

	// Los ajustes de embeddings quedaron guardados.
	got := map[string]string{}
	rows, err := tc.Pool.Query(ctx, "SELECT key, value FROM lodan_settings")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		got[k] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["embed_dims"] != "8" || got["embed_model"] != "test-model" {
		t.Errorf("lodan_settings = %v, se esperaba embed_dims=8 y embed_model=test-model", got)
	}

	// Otras dimensiones o otro modelo sobre el mismo clúster: error claro.
	for _, otro := range []Settings{
		{Dims: 16, Model: "test-model"},
		{Dims: 8, Model: "otro-modelo"},
	} {
		err := Migrate(ctx, tc.Pool, otro)
		if err == nil {
			t.Errorf("Migrate con %+v debería fallar", otro)
			continue
		}
		if !strings.Contains(err.Error(), "recalcular") {
			t.Errorf("Migrate con %+v: el error no explica el recálculo: %v", otro, err)
		}
	}

	// Tras los intentos fallidos, la configuración original sigue funcionando.
	if err := Migrate(ctx, tc.Pool, settings); err != nil {
		t.Fatalf("Migrate con la configuración original falló tras los errores: %v", err)
	}
}

func TestHalfVectorIdaYVuelta(t *testing.T) {
	tc := NewTestCluster(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Valores exactamente representables en media precisión.
	want := []float32{0.5, -1.25, 2, 0.125}

	var id int64
	err := tc.Pool.QueryRow(ctx,
		`INSERT INTO memories (kind, title, content, content_hash, embedding)
		 VALUES ('note', 'titulo', 'contenido', $1, $2) RETURNING id`,
		[]byte{1}, pgvector.NewHalfVector(want)).Scan(&id)
	if err != nil {
		t.Fatalf("no se pudo insertar un halfvec: %v", err)
	}

	var got pgvector.HalfVector
	if err := tc.Pool.QueryRow(ctx, "SELECT embedding FROM memories WHERE id = $1", id).Scan(&got); err != nil {
		t.Fatalf("no se pudo leer el halfvec: %v", err)
	}
	if len(got.Slice()) != len(want) {
		t.Fatalf("halfvec leído = %v, se esperaba %v", got.Slice(), want)
	}
	for i := range want {
		if got.Slice()[i] != want[i] {
			t.Errorf("halfvec[%d] = %v, se esperaba %v", i, got.Slice()[i], want[i])
		}
	}

	// Como parámetro de una consulta de distancia (uso habitual en recall).
	var dist float64
	err = tc.Pool.QueryRow(ctx, "SELECT embedding <=> $1 FROM memories WHERE id = $2",
		pgvector.NewHalfVector(want), id).Scan(&dist)
	if err != nil {
		t.Fatalf("no se pudo calcular la distancia coseno: %v", err)
	}
	if dist > 0.001 {
		t.Errorf("distancia coseno de un vector consigo mismo = %v, se esperaba ~0", dist)
	}

	// Embedding NULL: se lee con un puntero.
	_, err = tc.Pool.Exec(ctx,
		`INSERT INTO memories (kind, title, content, content_hash) VALUES ('note', 't2', 'c2', $1)`, []byte{2})
	if err != nil {
		t.Fatal(err)
	}
	var nullable *pgvector.HalfVector
	if err := tc.Pool.QueryRow(ctx, "SELECT embedding FROM memories WHERE title = 't2'").Scan(&nullable); err != nil {
		t.Fatalf("no se pudo leer un embedding NULL: %v", err)
	}
	if nullable != nil {
		t.Errorf("embedding NULL leído como %v, se esperaba nil", nullable.Slice())
	}

	// Una longitud distinta a la de la columna se rechaza.
	_, err = tc.Pool.Exec(ctx,
		`INSERT INTO memories (kind, title, content, content_hash, embedding) VALUES ('note', 't3', 'c3', $1, $2)`,
		[]byte{3}, pgvector.NewHalfVector([]float32{1, 2, 3}))
	if err == nil {
		t.Error("insertar un halfvec de 3 dimensiones en halfvec(4) debería fallar")
	}

	// Reset vacía las tablas de datos.
	tc.Reset(t)
	var n int
	if err := tc.Pool.QueryRow(ctx, "SELECT count(*) FROM memories").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("memories tiene %d filas tras Reset, se esperaba 0", n)
	}
}
