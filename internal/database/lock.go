package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	lockStaleAfter   = 2 * time.Minute
	lockRetryEvery   = 200 * time.Millisecond
	lockWaitDeadline = 60 * time.Second
)

// acquireLock takes a portable, cross-process lock by creating path with O_EXCL and writing
// the PID inside. A lock older than lockStaleAfter is considered abandoned: it is removed and
// the acquisition is retried. It polls every 200 ms for up to 60 s and returns the unlock
// function, which is safe to call more than once.
//
// Removing a stale lock is not atomic with re-creating it, so two processes that find the
// same stale lock at the same instant could in theory both proceed. The window is tiny and the
// protected operations (initdb, pg_ctl start, CREATE DATABASE) are idempotent.
func acquireLock(ctx context.Context, path string) (func(), error) {
	return acquireLockWait(ctx, path, lockWaitDeadline, "otro proceso de lodan está arrancando el clúster")
}

// acquireLockWait is acquireLock with a custom wait limit and a custom explanation for the
// timeout error (busy says who is probably holding the lock).
func acquireLockWait(ctx context.Context, path string, wait time.Duration, busy string) (func(), error) {
	deadline := time.Now().Add(wait)

	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
			cerr := f.Close()
			if werr != nil || cerr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("no se pudo escribir el lock %s: %w", path, errors.Join(werr, cerr))
			}
			var once sync.Once
			return func() { once.Do(func() { _ = os.Remove(path) }) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("no se pudo crear el lock %s: %w", path, err)
		}

		// The lock exists: drop it if it is stale, otherwise wait.
		if info, serr := os.Stat(path); serr == nil {
			if time.Since(info.ModTime()) > lockStaleAfter {
				_ = os.Remove(path)
				continue
			}
		} else if errors.Is(serr, fs.ErrNotExist) {
			// Released between OpenFile and Stat: retry right away.
			continue
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("tiempo de espera agotado (%s) esperando el lock %s: %s", wait, path, busy)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockRetryEvery):
		}
	}
}
