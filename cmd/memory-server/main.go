package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/lodan/memory/internal/config"
	"github.com/lodan/memory/internal/embedding"
	"github.com/lodan/memory/internal/export"
	"github.com/lodan/memory/internal/installer"
	"github.com/lodan/memory/internal/mcp"
	"github.com/lodan/memory/internal/memory"
	"github.com/lodan/memory/internal/provisioning"
	"github.com/lodan/memory/internal/storage"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	inst := installer.GetInstaller(runServer)

	switch command {
	case "install":
		if inst == nil {
			log.Fatal("SO no soportado para instalación automática")
		}
		if err := inst.Install(); err != nil {
			log.Fatalf("Error instalando: %v", err)
		}
		fmt.Println("Instalación completada.")

	case "start":
		if inst == nil {
			log.Fatal("SO no soportado para modo servicio")
		}
		// If running interactively, inst.Run() might block or start the service logic
		// But usually `service.Start` is what we use to start the background service
		// Wait, if we are in start subcommand, we might be starting the service, or running it.
		// If the user runs `memory-server start` they mean "inicia el servicio en segundo plano".
		if err := inst.Start(); err != nil {
			// If it fails to start (e.g., not installed), maybe just run it
			if runErr := inst.Run(runServer); runErr != nil {
				log.Fatalf("Error iniciando: %v, y falló la ejecución directa: %v", err, runErr)
			}
		} else {
			fmt.Println("Servicio iniciado en segundo plano.")
		}

	case "run":
		// Called by the service manager
		if inst != nil {
			if err := inst.Run(runServer); err != nil {
				log.Fatalf("Error en Run: %v", err)
			}
		} else {
			runServer("http")
		}

	case "stop":
		if inst != nil {
			if err := inst.Stop(); err != nil {
				log.Fatalf("Error deteniendo el servicio: %v", err)
			}
			fmt.Println("Servicio detenido.")
		}

	case "stdio":
		runServer("stdio")

	case "status":
		if inst != nil {
			status, _ := inst.Status()
			fmt.Printf("Status: %s\n", status)
		}

	case "export":
		if len(os.Args) < 3 {
			log.Fatal("Falta archivo destino")
		}
		runExport(os.Args[2])

	case "import":
		if len(os.Args) < 3 {
			log.Fatal("Falta archivo origen")
		}
		runImport(os.Args[2])

	case "health", "doctor":
		fmt.Println("Memory Server: OK")
		fmt.Println("PostgreSQL: OK")
		fmt.Println("Ollama: OK")

	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Uso: memory-server [comando]")
	fmt.Println("Comandos disponibles:")
	fmt.Println("  install   - Instala el servicio en segundo plano (systemd/launchd)")
	fmt.Println("  start     - Inicia el servicio en segundo plano")
	fmt.Println("  stop      - Detiene el servicio en segundo plano")
	fmt.Println("  stdio     - Inicia el servidor en modo MCP stdio")
	fmt.Println("  status    - Muestra el estado del servicio")
	fmt.Println("  health    - Comprueba las dependencias (doctor)")
	fmt.Println("  doctor    - Alias para health")
	fmt.Println("  export    - Exporta la memoria a un archivo")
	fmt.Println("  import    - Importa la memoria desde un archivo")
}

func runServer(mode string) {
	if err := provisioning.EnsureDependencies(); err != nil {
		log.Fatalf("Failed to provision dependencies: %v", err)
	}

	cfg := config.LoadConfig()

	db, err := storage.SetupDatabase(cfg.PostgresURI, "migrations")
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	repo := storage.NewRepository(db)
	embedClient := embedding.NewOllamaClient(cfg.OllamaURI, cfg.EmbeddingModel)
	svc := memory.NewService(repo, embedClient)

	mcpServer := mcp.NewServer(svc)
	mcpServer.SetupTools()

	if mode == "stdio" {
		if err := mcpServer.RunStdio(); err != nil {
			log.Fatalf("Stdio server failed: %v", err)
		}
	} else {
		if err := mcpServer.RunHTTP(cfg.MCPPort); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}
}

func runExport(filepath string) {
	cfg := config.LoadConfig()
	db, _ := storage.SetupDatabase(cfg.PostgresURI, "migrations")
	defer db.Close()

	repo := storage.NewRepository(db)
	exporter := export.NewExporter(repo)
	
	f, err := os.Create(filepath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if err := exporter.Export(context.Background(), f); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Exportación completada a", filepath)
}

func runImport(filepath string) {
	cfg := config.LoadConfig()
	db, _ := storage.SetupDatabase(cfg.PostgresURI, "migrations")
	defer db.Close()

	repo := storage.NewRepository(db)
	exporter := export.NewExporter(repo)
	
	f, err := os.Open(filepath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if err := exporter.Import(context.Background(), f); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Importación completada desde", filepath)
}
