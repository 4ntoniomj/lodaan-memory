package database

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"text/template"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrateLockKey is the constant advisory lock key ("lodanmig") held while migrating, so that
// several lodan processes starting at once do not apply migrations concurrently.
const migrateLockKey int64 = 0x6c6f64616e6d6967

var migrationNameRe = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// Settings are the embedding parameters the schema is built for. They are stored in the
// lodan_settings table the first time Migrate runs and must match on later runs.
type Settings struct {
	Dims  int
	Model string
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads the embedded migrations in version order and renders their templates
// ({{.Dims}} is the embedding dimension).
func loadMigrations(dims int) ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer las migraciones embebidas: %w", err)
	}

	var migs []migration
	for _, e := range entries { // ReadDir returns entries sorted by filename
		if e.IsDir() {
			continue
		}
		m := migrationNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("nombre de migración inválido %q: se espera NNNN_nombre.sql", e.Name())
		}
		version, _ := strconv.Atoi(m[1])
		if n := len(migs); n > 0 && migs[n-1].version >= version {
			return nil, fmt.Errorf("versión de migración duplicada o desordenada: %q", e.Name())
		}

		raw, err := migrationsFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("no se pudo leer la migración %s: %w", e.Name(), err)
		}
		tpl, err := template.New(e.Name()).Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("plantilla inválida en la migración %s: %w", e.Name(), err)
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, struct{ Dims int }{Dims: dims}); err != nil {
			return nil, fmt.Errorf("no se pudo renderizar la migración %s: %w", e.Name(), err)
		}
		migs = append(migs, migration{version: version, name: e.Name(), sql: buf.String()})
	}
	return migs, nil
}

// Migrate applies the pending embedded migrations, each in its own transaction and under an
// advisory lock. It is idempotent. It also records the embedding settings in lodan_settings the
// first time and fails if a later run uses a different model or dimension, because that
// requires recomputing all embeddings.
func Migrate(ctx context.Context, pool *pgxpool.Pool, s Settings) error {
	if s.Dims <= 0 {
		return fmt.Errorf("las dimensiones de los embeddings deben ser mayores que 0, y son %d", s.Dims)
	}
	if s.Model == "" {
		return fmt.Errorf("el nombre del modelo de embeddings no puede estar vacío")
	}
	migs, err := loadMigrations(s.Dims)
	if err != nil {
		return err
	}

	// The advisory lock is per session, so everything runs on one dedicated connection.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("no se pudo obtener una conexión para migrar: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("no se pudo tomar el lock de migraciones: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockKey)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version int PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("no se pudo crear schema_migrations: %w", err)
	}

	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("no se pudo leer schema_migrations: %w", err)
	}
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("no se pudo leer una versión de schema_migrations: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("no se pudo leer schema_migrations: %w", err)
	}

	// On an existing database, refuse to add migrations built for other settings.
	var hasSettings bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('lodan_settings') IS NOT NULL").Scan(&hasSettings); err != nil {
		return fmt.Errorf("no se pudo comprobar lodan_settings: %w", err)
	}
	if hasSettings {
		if err := syncSettings(ctx, conn, s); err != nil {
			return err
		}
	}

	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
	}

	return syncSettings(ctx, conn, s)
}

// applyMigration runs one migration and records it in schema_migrations, atomically.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("no se pudo abrir la transacción de la migración %s: %w", m.name, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // no-op once committed

	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("falló la migración %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.version); err != nil {
		return fmt.Errorf("no se pudo registrar la migración %s: %w", m.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("no se pudo confirmar la migración %s: %w", m.name, err)
	}
	return nil
}

// syncSettings stores the embedding settings if they are missing and checks that the stored
// ones match s.
func syncSettings(ctx context.Context, conn *pgxpool.Conn, s Settings) error {
	want := []struct{ key, value string }{
		{"embed_dims", strconv.Itoa(s.Dims)},
		{"embed_model", s.Model},
	}
	for _, kv := range want {
		if _, err := conn.Exec(ctx,
			"INSERT INTO lodan_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING",
			kv.key, kv.value); err != nil {
			return fmt.Errorf("no se pudo guardar el ajuste %s: %w", kv.key, err)
		}
		var got string
		if err := conn.QueryRow(ctx, "SELECT value FROM lodan_settings WHERE key = $1", kv.key).Scan(&got); err != nil {
			return fmt.Errorf("no se pudo leer el ajuste %s: %w", kv.key, err)
		}
		if got != kv.value {
			return fmt.Errorf("la base de datos se creó con %s = %q, pero la configuración actual es %q: "+
				"cambiar de modelo o de dimensiones requiere recalcular todos los embeddings", kv.key, got, kv.value)
		}
	}
	return nil
}
