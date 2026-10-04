package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultDataDir returns the per-user data directory for the current OS.
func DefaultDataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("la variable LOCALAPPDATA está vacía: no se puede determinar el directorio de datos")
		}
		return filepath.Join(base, "lodan"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no se pudo determinar el directorio personal: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "lodan"), nil
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "lodan"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no se pudo determinar el directorio personal: %w", err)
		}
		return filepath.Join(home, ".local", "share", "lodan"), nil
	}
}

// PGDataDir is the directory of the local PostgreSQL cluster.
func (c Config) PGDataDir() string { return filepath.Join(c.DataDir, "pg") }

// ConfigFile is the path of the JSON configuration file.
func (c Config) ConfigFile() string { return filepath.Join(c.DataDir, "config.json") }

// SecretFile is the path of the file holding the PostgreSQL password.
func (c Config) SecretFile() string { return filepath.Join(c.DataDir, "secret") }

// LogsDir is the directory where logs are written.
func (c Config) LogsDir() string { return filepath.Join(c.DataDir, "logs") }

// LockFile is the path of the lockfile used to serialize cluster startup.
func (c Config) LockFile() string { return filepath.Join(c.DataDir, "lodan.lock") }

// HeartbeatFile is the path of the file that the service supervisor touches while it is alive,
// so that a backup or restore knows it must hand PostgreSQL back to it.
func (c Config) HeartbeatFile() string { return filepath.Join(c.DataDir, "supervisor.heartbeat") }

// MaintenanceFile is the path of the mark that a backup or restore leaves while it hands the
// restart of PostgreSQL over to the service supervisor (see Cluster.MarkMaintenancePending).
func (c Config) MaintenanceFile() string { return filepath.Join(c.DataDir, "maintenance.pending") }
