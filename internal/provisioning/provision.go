package provisioning

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func findPostgresBinary(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err == nil {
		return path, nil
	}
	
	// Fallback for Debian/Ubuntu
	matches, _ := filepath.Glob(fmt.Sprintf("/usr/lib/postgresql/*/bin/%s", name))
	if len(matches) > 0 {
		// Take the highest version by taking the last match (Glob returns sorted)
		return matches[len(matches)-1], nil
	}
	
	return "", fmt.Errorf("%s not found", name)
}

func EnsureDependencies() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home dir: %w", err)
	}

	lodanDir := filepath.Join(home, ".lodan")
	dbDir := filepath.Join(lodanDir, "db")

	if err := os.MkdirAll(lodanDir, 0755); err != nil {
		return fmt.Errorf("failed to create lodan dir: %w", err)
	}

	// 1. Install PostgreSQL and Ollama if not present
	if err := installDependencies(); err != nil {
		return fmt.Errorf("failed to install dependencies: %w", err)
	}

	// 2. Initialize PostgreSQL if needed
	initdbPath, err := findPostgresBinary("initdb")
	if err != nil {
		return fmt.Errorf("initdb not found after installation: %w", err)
	}

	if _, err := os.Stat(filepath.Join(dbDir, "PG_VERSION")); os.IsNotExist(err) {
		log.Println("Initializing PostgreSQL database cluster at", dbDir)
		cmd := exec.Command(initdbPath, "-D", dbDir, "--auth=trust", "--auth-local=trust", "--auth-host=trust")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("initdb failed: %w", err)
		}

		confPath := filepath.Join(dbDir, "postgresql.conf")
		f, err := os.OpenFile(confPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			f.WriteString("\nport = 54320\nlisten_addresses = '127.0.0.1'\nunix_socket_directories = '/tmp'\n")
			f.Close()
		} else {
			log.Printf("failed to append to postgresql.conf: %v", err)
		}
	}

	// 3. Start PostgreSQL
	log.Println("Starting PostgreSQL...")
	pgCtlPath, err := findPostgresBinary("pg_ctl")
	if err != nil {
		return fmt.Errorf("pg_ctl not found after installation: %w", err)
	}
	
	if err := exec.Command(pgCtlPath, "-D", dbDir, "status").Run(); err != nil {
		pgCtl := exec.Command(pgCtlPath, "-D", dbDir, "-l", filepath.Join(lodanDir, "postgres.log"), "start")
		if err := pgCtl.Run(); err != nil {
			log.Printf("pg_ctl start warning (might be already running): %v", err)
		}
		
		// Wait a moment for postgres to be ready
		time.Sleep(2 * time.Second)
	}
	
	// Create database if not exists
	createdbPath, err := findPostgresBinary("createdb")
	if err != nil {
		return fmt.Errorf("createdb not found after installation: %w", err)
	}
	createCmd := exec.Command(createdbPath, "-h", "127.0.0.1", "-p", "54320", "lodan")
	createCmd.Stdout = os.Stdout
	createCmd.Stderr = os.Stderr
	if err := createCmd.Run(); err != nil {
		// Ignore error here as DB might exist, but log it
		log.Printf("createdb info: %v", err)
	}

	// 4. Start Ollama if not running
	log.Println("Starting Ollama...")
	if err := exec.Command("curl", "-s", "http://localhost:11434/api/version").Run(); err != nil {
		// Start in background
		ollamaCmd := exec.Command("ollama", "serve")
		ollamaCmd.Stdout = os.Stdout
		ollamaCmd.Stderr = os.Stderr
		if err := ollamaCmd.Start(); err != nil {
			return fmt.Errorf("failed to start ollama: %w", err)
		}
		time.Sleep(3 * time.Second) // wait for it to start
	}

	// 5. Pull model
	log.Println("Checking ollama model embeddinggemma:300m-qat-q4_0...")
	out, _ := exec.Command("ollama", "list").Output()
	if !strings.Contains(string(out), "embeddinggemma:300m-qat-q4_0") {
		log.Println("Pulling ollama model embeddinggemma:300m-qat-q4_0...")
		pullCmd := exec.Command("ollama", "pull", "embeddinggemma:300m-qat-q4_0")
		pullCmd.Stdout = os.Stdout
		pullCmd.Stderr = os.Stderr
		if err := pullCmd.Run(); err != nil {
			return fmt.Errorf("failed to pull model: %w", err)
		}
	}

	return nil
}

func installDependencies() error {
	// Check Postgres
	_, errPgInit := findPostgresBinary("initdb")
	_, errPgCtl := findPostgresBinary("pg_ctl")
	_, errPg := findPostgresBinary("postgres")
	_, errOllama := exec.LookPath("ollama")
	
	if errPgInit == nil && errPgCtl == nil && errPg == nil && errOllama == nil {
		return nil
	}

	// Use brew or apt
	hasBrew := false
	if _, err := exec.LookPath("brew"); err == nil {
		hasBrew = true
	}

	hasApt := false
	if _, err := exec.LookPath("apt-get"); err == nil {
		hasApt = true
	}

	if errPgInit != nil || errPgCtl != nil || errPg != nil {
		log.Println("Installing PostgreSQL...")
		if hasBrew {
			cmd := exec.Command("brew", "install", "postgresql@18", "pgvector")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("fallo al instalar dependencias: %w", err)
			}
		} else if hasApt {
			cmdUp := exec.Command("sudo", "apt-get", "update")
			cmdUp.Stdout = os.Stdout
			cmdUp.Stderr = os.Stderr
			if err := cmdUp.Run(); err != nil {
				return fmt.Errorf("fallo al instalar dependencias: %w", err)
			}
			
			log.Println("Note: Attempting to install postgresql, postgresql-contrib and postgresql-all for pgvector")
			cmdInst := exec.Command("sudo", "apt-get", "install", "-y", "postgresql", "postgresql-contrib", "postgresql-all")
			cmdInst.Stdout = os.Stdout
			cmdInst.Stderr = os.Stderr
			if err := cmdInst.Run(); err != nil {
				return fmt.Errorf("fallo al instalar dependencias: %w", err)
			}
		} else {
			return fmt.Errorf("no supported package manager found to install PostgreSQL")
		}
	}

	if errOllama != nil {
		log.Println("Installing Ollama...")
		// Use standard install script
		cmd := exec.Command("sh", "-c", "curl -fsSL https://ollama.com/install.sh | sh")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install ollama: %w", err)
		}
	}

	return nil
}
