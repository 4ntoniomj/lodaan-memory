package database

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// lodanMaxConnRe matches the max_connections line inside the block lodan appends.
var lodanMaxConnRe = regexp.MustCompile(`(?m)^max_connections = \d+$`)

// setLodanMaxConnections rewrites the max_connections line of lodan's block, simulating a
// cluster created by an older lodan (or edited by hand).
func setLodanMaxConnections(t *testing.T, pgdata, value string) {
	t.Helper()
	path := filepath.Join(pgdata, "postgresql.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !lodanMaxConnRe.Match(data) {
		t.Fatalf("postgresql.conf no contiene la línea max_connections de lodan:\n%s", data)
	}
	data = lodanMaxConnRe.ReplaceAll(data, []byte("max_connections = "+value))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// openConns opens n simultaneous connections and fails the test if any is rejected.
func openConns(ctx context.Context, t *testing.T, dsn string, n int) {
	t.Helper()
	conns := make([]*pgx.Conn, 0, n)
	defer func() {
		for _, c := range conns {
			_ = c.Close(context.Background())
		}
	}()
	for i := 0; i < n; i++ {
		c, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("la conexión simultánea %d de %d fue rechazada: %v", i+1, n, err)
		}
		conns = append(conns, c)
	}
}

// Many AI clients run their own `lodan serve` at the same time: the cluster must accept far
// more than 20 connections, both when it is created and when an old cluster is upgraded.
func TestClusterAceptaMuchasConexionesSimultaneas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*testTimeout)
	defer cancel()

	cl, cfg := newLocalCluster(t, 4)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testTimeout)
		defer stopCancel()
		_ = cl.Stop(stopCtx)
	})

	check := func(stage string) {
		t.Helper()
		if err := cl.Start(ctx); err != nil {
			t.Fatalf("%s: Start falló: %v", stage, err)
		}
		dsn, err := cl.DSN("postgres")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("%s: conexión falló: %v", stage, err)
		}
		got := show(ctx, t, conn, "max_connections")
		_ = conn.Close(context.Background())
		if got != "100" {
			t.Fatalf("%s: max_connections = %s, se esperaba 100", stage, got)
		}
		openConns(ctx, t, dsn, 40)
	}

	// New cluster.
	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init falló: %v", err)
	}
	check("clúster nuevo")

	// Cluster created by an older lodan with max_connections = 20: Init upgrades it and the
	// next start applies it.
	if err := cl.Stop(ctx); err != nil {
		t.Fatalf("Stop falló: %v", err)
	}
	setLodanMaxConnections(t, cfg.PGDataDir(), "20")
	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init sobre clúster antiguo falló: %v", err)
	}
	check("clúster antiguo actualizado")

	// Init stays idempotent: the block is not duplicated.
	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init no es idempotente: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.PGDataDir(), "postgresql.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(lodanMaxConnRe.FindAll(data, -1)); n != 1 {
		t.Errorf("postgresql.conf tiene %d líneas max_connections de lodan, se esperaba 1", n)
	}
}

// A value the user set by hand is respected: only lodan's old default is upgraded.
func TestInitRespetaMaxConnectionsPersonalizado(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	cl, cfg := newLocalCluster(t, 4)
	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init falló: %v", err)
	}
	setLodanMaxConnections(t, cfg.PGDataDir(), "300")
	if err := cl.Init(ctx); err != nil {
		t.Fatalf("Init falló: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.PGDataDir(), "postgresql.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "max_connections = 300") {
		t.Errorf("Init cambió un max_connections personalizado:\n%s", data)
	}
}

// An idle `lodan serve` (an AI that is not using memory right now) must give its
// connections back instead of holding them for the default 30 minutes.
func TestPoolLiberaConexionesInactivas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	cl, _ := newLocalCluster(t, 4)
	if err := cl.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning falló: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testTimeout)
		defer stopCancel()
		_ = cl.Stop(stopCtx)
	})
	dsn, err := cl.DSN(defaultDatabase)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open falló: %v", err)
	}
	defer pool.Close()

	pc := pool.Config()
	if pc.MinConns != 0 {
		t.Errorf("MinConns = %d, se esperaba 0", pc.MinConns)
	}
	if pc.MaxConnIdleTime <= 0 || pc.MaxConnIdleTime > time.Minute {
		t.Errorf("MaxConnIdleTime = %s, se esperaba entre 0 y 1m", pc.MaxConnIdleTime)
	}
	if pc.HealthCheckPeriod <= 0 || pc.HealthCheckPeriod > 30*time.Second {
		t.Errorf("HealthCheckPeriod = %s, se esperaba entre 0 y 30s", pc.HealthCheckPeriod)
	}
}
