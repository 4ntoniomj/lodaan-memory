package gitsync

import (
	"os"
	"regexp"
	"strconv"
	"strings"

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

// String is the line shown to the user for a contradiction found while importing.
func (k Contradiction) String() string {
	return "contradicción en la clave " + k.Key + ": #" + strconv.FormatInt(k.KeptID, 10) + " conserva la clave, #" +
		strconv.FormatInt(k.OtherID, 10) + " no; dime cuál es el bueno"
}
