package gitsync_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/gitsync"
	"lodan/internal/memory"
)

// holdLock takes the advisory lock gitsync.LockKey on a dedicated connection of the pool and
// returns the function that releases it.
func holdLock(t *testing.T, pool *pgxpool.Pool) (release func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(gitsync.LockKey)); err != nil {
		conn.Release()
		t.Fatalf("pg_advisory_lock: %v", err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		var ok bool
		if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, int64(gitsync.LockKey)).Scan(&ok); err != nil || !ok {
			t.Errorf("pg_advisory_unlock: ok=%v err=%v", ok, err)
		}
		conn.Release()
	}
	t.Cleanup(release)
	return release
}

// requireLockFree checks that no advisory lock is held on the database and that LockKey can be taken.
func requireLockFree(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var held int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'`).Scan(&held); err != nil {
		t.Fatalf("pg_locks: %v", err)
	}
	if held != 0 {
		t.Errorf("locks consultivos retenidos tras la operación = %d, se esperaba 0", held)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, int64(gitsync.LockKey)).Scan(&got); err != nil {
		t.Fatalf("pg_try_advisory_lock: %v", err)
	}
	if !got {
		t.Error("pg_try_advisory_lock(LockKey) = false: la operación dejó el lock tomado")
		return
	}
	_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, int64(gitsync.LockKey))
}

// S17 (a): Pull waits for the advisory lock and gives up with the context error; imports once it is free.
func TestLockPullEsperaYAbandona(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	p := newPair(t)
	ca, cb := cfgFor(t, bare, p.a.id), cfgFor(t, bare, p.b.id)
	sharedKey(t, ca, cb)
	for _, c := range []gitsync.Config{ca, cb} {
		if _, err := gitsync.Init(gctx(t), c); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}
	p.a.rem(t, "Titulo con lock", "contenido del registro que viaja con lock", memory.KindFact, "")
	if _, err := gitsync.Push(gctx(t), p.a.pool, ca); err != nil {
		t.Fatalf("Push de A: %v", err)
	}

	release := holdLock(t, p.b.pool)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := gitsync.Pull(ctx, p.b.pool, cb)
	if err == nil {
		t.Fatal("Pull con el lock tomado por otra conexión devolvió nil, se esperaba un error")
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("Pull tardó %v en abandonar, se esperaba ~2 s", d)
	}
	if n := p.b.count(t, "title = $1", "Titulo con lock"); n != 0 {
		t.Errorf("registros importados con el lock tomado = %d, se esperaba 0", n)
	}

	release()
	res, err := gitsync.Pull(gctx(t), p.b.pool, cb)
	if err != nil {
		t.Fatalf("Pull tras liberar el lock: %v", err)
	}
	if res.Files != 1 {
		t.Errorf("Pull = %+v, se esperaba 1 fichero", res)
	}
	if n := p.b.count(t, "title = $1", "Titulo con lock"); n != 1 {
		t.Errorf("registros en B tras liberar = %d, se esperaba 1", n)
	}
}

// S17 (a): Push waits for the advisory lock and gives up with the context error; pushes once it is free.
func TestLockPushEsperaYAbandona(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	m := newMachine(t, "maquina-a", "cliente-a")
	c := cfgFor(t, bare, m.id)
	if _, err := gitsync.Init(gctx(t), c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	m.rem(t, "Uno con lock", "contenido que no debe salir mientras hay lock", memory.KindFact, "")

	release := holdLock(t, m.pool)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := gitsync.Push(ctx, m.pool, c)
	if err == nil {
		t.Fatal("Push con el lock tomado por otra conexión devolvió nil, se esperaba un error")
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("Push tardó %v en abandonar, se esperaba ~2 s", d)
	}
	if n := commitCount(t, bare); n != 0 {
		t.Errorf("commits en el remoto con el lock tomado = %d, se esperaba 0", n)
	}

	release()
	res, err := gitsync.Push(gctx(t), m.pool, c)
	if err != nil {
		t.Fatalf("Push tras liberar el lock: %v", err)
	}
	if res.File == "" || res.Memories != 1 {
		t.Errorf("Push = %+v, se esperaba un fichero con 1 memoria", res)
	}
	if n := commitCount(t, bare); n != 1 {
		t.Errorf("commits en el remoto tras liberar = %d, se esperaba 1", n)
	}
}

// S17 (a): two concurrent pulls of the same machine and working copy both succeed and import once.
func TestLockDosPullsConcurrentes(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	p := newPair(t)
	ca, cb := cfgFor(t, bare, p.a.id), cfgFor(t, bare, p.b.id)
	sharedKey(t, ca, cb)
	for _, c := range []gitsync.Config{ca, cb} {
		if _, err := gitsync.Init(gctx(t), c); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}
	p.a.rem(t, "Concurrente", "contenido importado exactamente una vez", memory.KindFact, "")
	if _, err := gitsync.Push(gctx(t), p.a.pool, ca); err != nil {
		t.Fatalf("Push de A: %v", err)
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), opTime)
			defer cancel()
			_, errs[i] = gitsync.Pull(ctx, p.b.pool, cb)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Pull concurrente #%d: %v", i+1, err)
		}
	}
	if n := p.b.count(t, "title = $1", "Concurrente"); n != 1 {
		t.Errorf("registros en B = %d, se esperaba 1", n)
	}
}

// S17 (a): after Push and Pull return, the lock is free.
func TestLockLiberadoTrasPushYPull(t *testing.T) {
	gitEnv(t)
	bare := newBare(t)
	p := newPair(t)
	ca, cb := cfgFor(t, bare, p.a.id), cfgFor(t, bare, p.b.id)
	sharedKey(t, ca, cb)
	for _, c := range []gitsync.Config{ca, cb} {
		if _, err := gitsync.Init(gctx(t), c); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}
	p.a.rem(t, "Libera", "contenido para comprobar la liberación del lock", memory.KindFact, "")
	if _, err := gitsync.Push(gctx(t), p.a.pool, ca); err != nil {
		t.Fatalf("Push: %v", err)
	}
	requireLockFree(t, p.a.pool)

	if _, err := gitsync.Pull(gctx(t), p.b.pool, cb); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	requireLockFree(t, p.b.pool)
}
