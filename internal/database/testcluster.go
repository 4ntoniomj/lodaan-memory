package database

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/config"
)

// testTimeout bounds each cluster operation performed by the test helpers.
const testTimeout = 90 * time.Second

// TestCluster is a throwaway PostgreSQL cluster with the lodan schema applied, for tests of
// any package. It lives in a temporary directory and is stopped when the test ends.
type TestCluster struct {
	Cluster *Cluster
	Cfg     config.Config
	Pool    *pgxpool.Pool
}

// newLocalCluster builds a Cluster over a temporary data directory and a free TCP port.
// It skips the test if the PostgreSQL binaries cannot be found.
func newLocalCluster(t testing.TB, dims int) (*Cluster, config.Config) {
	t.Helper()

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.EmbedDims = dims
	cfg.EmbedModel = "test-model"
	// config.Default leaves PGBinDir empty; honour LODAN_PG_BIN_DIR so tests can use
	// binaries that are not in the PATH (e.g. the ones installed by micromamba).
	if dir := os.Getenv("LODAN_PG_BIN_DIR"); dir != "" {
		cfg.PGBinDir = dir
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo reservar un puerto libre: %v", err)
	}
	cfg.PGPort = l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("no se pudo liberar el puerto reservado: %v", err)
	}

	cl, err := NewCluster(cfg)
	if err != nil {
		t.Skipf("se omite el test: no hay binarios de PostgreSQL: %v", err)
	}
	return cl, cfg
}

// NewTestCluster creates a temporary cluster, starts it, creates the "lodan" database, opens a
// pool and applies the migrations for embeddings of the given dimension (model "test-model").
// The test is skipped if PostgreSQL or the pgvector extension is not available. Everything is
// torn down with t.Cleanup.
func NewTestCluster(t testing.TB, dims int) *TestCluster {
	t.Helper()

	cl, cfg := newLocalCluster(t, dims)
	tc := &TestCluster{Cluster: cl, Cfg: cfg}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init falló: %v", err)
	}
	if err := cl.Start(ctx); err != nil {
		t.Fatalf("Start falló: %v", err)
	}
	t.Cleanup(func() {
		if tc.Pool != nil {
			tc.Pool.Close()
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testTimeout)
		defer stopCancel()
		if err := cl.Stop(stopCtx); err != nil {
			t.Logf("no se pudo parar el clúster de prueba: %v", err)
		}
	})

	if err := cl.EnsureDatabase(ctx, defaultDatabase); err != nil {
		t.Fatalf("EnsureDatabase falló: %v", err)
	}
	dsn, err := cl.DSN(defaultDatabase)
	if err != nil {
		t.Fatalf("DSN falló: %v", err)
	}
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open falló: %v", err)
	}
	tc.Pool = pool

	var haveVector bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector')").Scan(&haveVector); err != nil {
		t.Fatalf("no se pudo consultar pg_available_extensions: %v", err)
	}
	if !haveVector {
		t.Skip("se omite el test: la extensión pgvector (vector) no está disponible en este PostgreSQL")
	}

	if err := Migrate(ctx, pool, Settings{Dims: dims, Model: cfg.EmbedModel}); err != nil {
		t.Fatalf("Migrate falló: %v", err)
	}
	return tc
}

// Reset empties every data table and restarts the identity counters, keeping the schema
// (and lodan_settings) intact.
func (tc *TestCluster) Reset(t testing.TB) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, err := tc.Pool.Exec(ctx, `TRUNCATE memories, sessions, topics, memory_topics, session_topics,
		memory_relations, embedding_models RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("no se pudieron vaciar las tablas: %v", err)
	}
}
