// Command lodan is the entry point of the lodan memory server.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// version is the release version. It is "dev" unless set at build time with
// -ldflags "-X main.version=...".
var version = "dev"

const usageText = `lodan: servidor de memoria para asistentes de IA

Uso:
  lodan <subcomando> [opciones]

Subcomandos:
  serve     Arranca el servidor MCP (stdio por defecto, --http para HTTP local)
  db        Gestiona el PostgreSQL local: init | start | stop | status | backup | restore
  migrate   Aplica las migraciones pendientes
  status    Muestra el estado de la base de datos y de Ollama
  install   Instala lodan: PostgreSQL, Ollama, servicio de sistema, clientes MCP y skill
  doctor    Diagnostica la instalación, con una línea por comprobación
  uninstall Desinstala lodan (los datos se conservan salvo con --purge)
  service   Gestiona el servicio de sistema: start | stop | restart | enable | disable | status
  bench     Ejecuta el benchmark (--rows, --out)

Copias de seguridad (paran PostgreSQL solo mientras se copian los datos; la compresión
se hace con PostgreSQL ya en marcha y el destino necesita espacio libre para esa copia):
  lodan db backup [--to destino] [--full|--incremental|--differential]
      Crea destino/lodan_backup_AAAA-MM-DD_HHMMSS.<full|incr|diff>.tar.zst
      (por defecto un backup full en el directorio temporal del sistema).
      --differential: cambios desde el último full del destino.
      --incremental: cambios desde el último backup del destino, de cualquier tipo.
      Ambos requieren un full previo en el destino.
  lodan db restore --from archivo1.tar.zst [archivo2.tar.zst ...]
  lodan db restore --from /carpeta
      Restaura un full y, opcionalmente, incrementales y diferenciales (del más antiguo
      al más reciente). Con una carpeta usa el último full y los backups posteriores.

Usa "lodan <subcomando> -h" para ver las opciones de cada uno.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}

	name, rest := args[0], args[1:]
	switch name {
	case "-h", "--help", "-help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	case "serve":
		return runServe(rest, stderr)
	case "db":
		return runDB(rest, stdout, stderr)
	case "migrate":
		return runMigrate(rest, stdout, stderr)
	case "status":
		return runStatus(rest, stdout, stderr)
	case "service":
		return runService(rest, stdout, stderr)
	case "install":
		return runInstall(rest, stdout, stderr)
	case "uninstall":
		return runUninstall(rest, stdout, stderr)
	case "doctor":
		return runDoctor(rest, stdout, stderr)
	case "bench":
		return runBench(rest, stderr)
	default:
		fmt.Fprintf(stderr, "lodan: subcomando desconocido %q\n\n", name)
		fmt.Fprint(stderr, usageText)
		return 2
	}
}

// newFlagSet creates a FlagSet that reports errors and help to w.
func newFlagSet(name, usage string, w io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(w)
	fs.Usage = func() {
		fmt.Fprintf(w, "Uso: %s\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags parses args. When done is true, the caller must return code.
func parseFlags(fs *flag.FlagSet, args []string) (code int, done bool) {
	err := fs.Parse(args)
	switch {
	case err == nil:
		return 0, false
	case errors.Is(err, flag.ErrHelp):
		return 0, true
	default:
		return 2, true
	}
}
