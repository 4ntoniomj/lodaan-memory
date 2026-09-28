package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "github.com/lib/pq"
)

func SetupDatabase(uri string, migrationsDir string) (*sql.DB, error) {
	db, err := sql.Open("postgres", uri)
	if err != nil {
		return nil, fmt.Errorf("failed to open db: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping db: %w", err)
	}

	if err := runMigrations(db, migrationsDir); err != nil {
		return nil, fmt.Errorf("migrations failed: %w", err)
	}

	return db, nil
}

func runMigrations(db *sql.DB, dir string) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if filepath.Ext(file.Name()) == ".sql" {
			path := filepath.Join(dir, file.Name())
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			
			log.Printf("Running migration: %s", file.Name())
			if _, err := db.ExecContext(context.Background(), string(content)); err != nil {
				return fmt.Errorf("failed to execute %s: %w", file.Name(), err)
			}
		}
	}
	return nil
}
