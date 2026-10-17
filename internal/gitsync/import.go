package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pendingFile is a batch file not imported yet.
type pendingFile struct {
	rel     string // relative to Dir, forward slashes
	stamp   string
	n       int64
	machine string
}

// Import applies every not-yet-imported file of the other machines' folders under
// opts.Dir/changes/, oldest first (by file name). Undecryptable files are reported in
// ImportResult.Errors and retried on the next run; the other files are still applied.
func Import(ctx context.Context, pool *pgxpool.Pool, opts ImportOptions) (ImportResult, error) {
	var res ImportResult
	if err := checkMachineID(opts.MachineID); err != nil {
		return res, err
	}
	identity, err := age.ParseX25519Identity(opts.Identity)
	if err != nil {
		return res, errors.New("la clave de sincronización no es válida")
	}

	files, err := listPending(ctx, pool, opts)
	if err != nil {
		return res, err
	}
	for _, f := range files {
		b, err := readBatch(filepath.Join(opts.Dir, filepath.FromSlash(f.rel)), identity)
		if err != nil {
			var nm *age.NoIdentityMatchError
			if errors.As(err, &nm) {
				res.Errors = append(res.Errors, "no se pudo descifrar "+f.rel+": la clave no coincide")
			} else {
				res.Errors = append(res.Errors, "no se pudo leer "+f.rel+": "+err.Error())
			}
			continue
		}
		contradictions, err := applyFile(ctx, pool, f.rel, b)
		if err != nil {
			return res, fmt.Errorf("no se pudo importar %s: %w", f.rel, err)
		}
		res.Files++
		res.Contradictions = append(res.Contradictions, contradictions...)
	}

	if err := inTx(ctx, pool, func(a *applier) error { return a.retryPendingRelations(ctx) }); err != nil {
		return res, fmt.Errorf("no se pudieron aplicar las relaciones pendientes: %w", err)
	}
	return res, nil
}

// listPending returns the files of the other machines that were not imported yet, oldest first.
func listPending(ctx context.Context, pool *pgxpool.Pool, opts ImportOptions) ([]pendingFile, error) {
	root := filepath.Join(opts.Dir, "changes")
	machines, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer la carpeta de cambios: %w", err)
	}
	var all []pendingFile
	for _, m := range machines {
		if !m.IsDir() || m.Name() == opts.MachineID {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, m.Name()))
		if err != nil {
			return nil, fmt.Errorf("no se pudo leer la carpeta de %s: %w", m.Name(), err)
		}
		for _, e := range entries {
			stamp, n, ok := parseFileName(e.Name())
			if e.IsDir() || !ok {
				continue
			}
			all = append(all, pendingFile{rel: "changes/" + m.Name() + "/" + e.Name(), stamp: stamp, n: n, machine: m.Name()})
		}
	}
	if len(all) == 0 {
		return nil, nil
	}

	paths := make([]string, len(all))
	for i, f := range all {
		paths[i] = f.rel
	}
	rows, err := pool.Query(ctx, `SELECT path FROM sync_imported_files WHERE path = ANY($1)`, paths)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los ficheros importados: %w", err)
	}
	done, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los ficheros importados: %w", err)
	}
	seen := make(map[string]bool, len(done))
	for _, p := range done {
		seen[p] = true
	}
	var pending []pendingFile
	for _, f := range all {
		if !seen[f.rel] {
			pending = append(pending, f)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		a, b := pending[i], pending[j]
		if a.stamp != b.stamp {
			return a.stamp < b.stamp
		}
		if a.n != b.n {
			return a.n < b.n
		}
		return a.machine < b.machine
	})
	return pending, nil
}

// readBatch decrypts and decodes one batch file.
func readBatch(path string, identity age.Identity) (batch, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return batch{}, err
	}
	r, err := age.Decrypt(bytes.NewReader(raw), identity)
	if err != nil {
		return batch{}, err
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return batch{}, err
	}
	return decodeBatch(bytes.Split(plain, []byte("\n")))
}

// applyFile applies one batch in a single transaction and records the file as imported.
func applyFile(ctx context.Context, pool *pgxpool.Pool, rel string, b batch) ([]Contradiction, error) {
	var contradictions []Contradiction
	err := inTx(ctx, pool, func(a *applier) error {
		for _, l := range b.sessions {
			if err := a.applySession(ctx, l); err != nil {
				return err
			}
		}
		for _, l := range b.memories {
			if err := a.applyMemory(ctx, l); err != nil {
				return err
			}
		}
		for _, l := range b.relations {
			if err := a.applyRelation(ctx, l); err != nil {
				return err
			}
		}
		for _, l := range b.deletions {
			if err := a.applyDeletion(ctx, l); err != nil {
				return err
			}
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO sync_imported_files (path) VALUES ($1) ON CONFLICT DO NOTHING`, rel); err != nil {
			return fmt.Errorf("no se pudo registrar el fichero importado: %w", err)
		}
		contradictions = a.contradictions
		return nil
	})
	return contradictions, err
}

// inTx runs fn in a transaction marked as an import, so its changes are not exported again.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(a *applier) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL lodan.sync_import = 'on'`); err != nil {
		return err
	}
	a := &applier{tx: tx, topics: map[string]int32{}}
	if err := fn(a); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
