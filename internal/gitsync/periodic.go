package gitsync

import (
	"context"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/config"
)

// FromConfig builds the sync configuration from the lodan configuration. ok is false when
// sync has not been set up (no repository).
func FromConfig(cfg config.Config) (c Config, ok bool, err error) {
	if cfg.SyncRepo == "" {
		return Config{}, false, nil
	}
	c = Config{RepoURL: cfg.SyncRepo, Dir: cfg.SyncDir(), MachineID: cfg.SyncMachine, KeyFile: cfg.SyncKeyFile}
	if c.MachineID == "" {
		c.MachineID = DefaultMachineID()
	}
	if c.KeyFile == "" {
		if c.KeyFile, err = config.DefaultSyncKeyFile(); err != nil {
			return Config{}, false, err
		}
	}
	return c, true, nil
}

var notMachineChars = regexp.MustCompile(`[^a-z0-9-]+`)

// DefaultMachineID is the host name in lower case with anything other than letters, digits
// and hyphens replaced by hyphens.
func DefaultMachineID() string {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	id := strings.Trim(notMachineChars.ReplaceAllString(strings.ToLower(host), "-"), "-")
	if len(id) > 64 {
		id = id[:64]
	}
	if id == "" {
		id = "maquina"
	}
	return id
}

// RunPeriodic pushes and then pulls every interval until ctx is cancelled. Failures are
// logged and never stop the caller.
func RunPeriodic(ctx context.Context, pool *pgxpool.Pool, c Config, interval time.Duration, logger *log.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if res, err := Push(ctx, pool, c); err != nil {
			logger.Printf("sincronización: no se pudo enviar: %v", err)
		} else if res.File != "" {
			logger.Printf("sincronización: enviado %s", res.File)
		}
		res, err := Pull(ctx, pool, c)
		if err != nil {
			logger.Printf("sincronización: no se pudo recibir: %v", err)
			continue
		}
		for _, e := range res.Errors {
			logger.Printf("sincronización: %s", e)
		}
		for _, k := range res.Contradictions {
			logger.Printf("sincronización: %s", k)
		}
		if res.Files > 0 {
			logger.Printf("sincronización: importados %d ficheros", res.Files)
		}
	}
}

// String is the line shown to the user for a contradiction found while importing.
func (k Contradiction) String() string {
	return "contradicción en la clave " + k.Key + ": #" + strconv.FormatInt(k.KeptID, 10) + " conserva la clave, #" +
		strconv.FormatInt(k.OtherID, 10) + " no; dime cuál es el bueno"
}
