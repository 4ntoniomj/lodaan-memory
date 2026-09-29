package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lodan/internal/config"
	"lodan/internal/service"
)

// Level is the severity of a doctor check.
type Level int

const (
	// LevelOK means the check passed.
	LevelOK Level = iota
	// LevelWarn means something is off but lodan can still work.
	LevelWarn
	// LevelError means lodan will not work properly until it is fixed.
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelOK:
		return "ok"
	case LevelWarn:
		return "aviso"
	default:
		return "error"
	}
}

// CheckResult is the outcome of one doctor check.
type CheckResult struct {
	Name   string
	Level  Level
	Detail string
	// Fix is the proposed solution; empty when Level is LevelOK.
	Fix string
}

// DoctorHasErrors reports whether any check ended in error.
func DoctorHasErrors(results []CheckResult) bool {
	for _, r := range results {
		if r.Level == LevelError {
			return true
		}
	}
	return false
}

// doctorTimeout bounds each check that talks to PostgreSQL.
const doctorTimeout = 15 * time.Second

// adminCmd formats a command that needs administrator privileges.
func (in *Installer) adminCmd(cmd string) string {
	if in.Env.GOOS == "windows" {
		return cmd + " (en una consola de administrador)"
	}
	return "sudo " + cmd
}

// Doctor runs every check, prints one line per check and returns the results.
// It never changes anything.
func (in *Installer) Doctor(ctx context.Context) []CheckResult {
	var results []CheckResult
	add := func(name string, level Level, detail, fix string) {
		r := CheckResult{Name: name, Level: level, Detail: detail, Fix: fix}
		results = append(results, r)
		line := fmt.Sprintf("%-6s %s: %s", r.Level, r.Name, r.Detail)
		if r.Level != LevelOK && r.Fix != "" {
			line += " → Solución: " + r.Fix
		}
		in.printf("%s\n", line)
	}

	in.printf("lodan: diagnóstico\nDatos en %s\n\n", in.Cfg.DataDir)
	bin := StableBinary(in.Cfg.DataDir, in.Env.GOOS)
	reinstall := "ejecuta «lodan install»"

	// 1. Stable binary.
	switch st, err := os.Stat(bin); {
	case err != nil:
		add("Binario estable", LevelError, "no existe "+bin, reinstall)
	case !st.Mode().IsRegular():
		add("Binario estable", LevelError, bin+" no es un archivo normal", reinstall)
	case in.Env.GOOS != "windows" && st.Mode().Perm()&0o111 == 0:
		add("Binario estable", LevelError, bin+" no es ejecutable", reinstall)
	default:
		add("Binario estable", LevelOK, bin, "")
	}

	// 2. PostgreSQL binaries and pgvector files.
	rt := in.newRuntime(in.runtimeDir())
	pgDir, pgErr := config.DetectPGBinDir(in.Cfg)
	switch {
	case pgErr != nil:
		add("PostgreSQL", LevelError, pgErr.Error(), reinstall)
	case pgDir == rt.PostgresBinDir():
		if missing := rt.MissingPostgresFiles(); len(missing) > 0 {
			add("PostgreSQL", LevelError, "faltan archivos: "+strings.Join(missing, ", "), reinstall)
		} else {
			add("PostgreSQL", LevelOK, "binarios y vector.control en "+pgDir, "")
		}
	default:
		var missing []string
		for _, name := range []string{"initdb", "pg_ctl", "postgres"} {
			if p := filepath.Join(pgDir, name+exeSuffix(in.Env.GOOS)); !isRegularFile(p) {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			add("PostgreSQL", LevelError, "faltan archivos: "+strings.Join(missing, ", "), reinstall)
		} else {
			add("PostgreSQL", LevelOK, "binarios en "+pgDir+" (pgvector se comprueba con la conexión)", "")
		}
	}

	// 3. Cluster.
	cl, clErr := in.newCluster(in.Cfg)
	switch {
	case clErr != nil:
		add("Clúster", LevelError, "no se puede comprobar: "+clErr.Error(), reinstall)
	case !cl.Initialized():
		add("Clúster", LevelError, "no está inicializado en "+in.Cfg.PGDataDir(), reinstall)
	default:
		add("Clúster", LevelOK, "inicializado en "+in.Cfg.PGDataDir(), "")
	}

	// 4. lodan service.
	serviceInstalled := in.checkService(service.DefaultName, true, add)

	// 5. Connection and pgvector.
	switch {
	case clErr != nil:
		add("Base de datos", LevelError, "no se puede comprobar sin los binarios de PostgreSQL", reinstall)
	default:
		in.checkDatabase(ctx, cl, serviceInstalled, add)
	}

	// 6. Ollama, model and test embedding.
	in.checkOllama(ctx, add)

	// 7. lodan-ollama service, only if lodan installed that Ollama.
	if _, err := in.ollama().Find(in.ollamaInstallDir()); err == nil {
		in.checkService(OllamaServiceName, false, add)
	}

	// 8. Clients.
	if detected := DetectClients(in.Env); len(detected) == 0 {
		add("Clientes MCP", LevelOK, "no se ha detectado ninguno", "")
	} else {
		for _, c := range detected {
			if ok, detail := Check(c, bin); ok {
				add("Cliente "+c.Name, LevelOK, detail, "")
			} else {
				add("Cliente "+c.Name, LevelWarn, detail, reinstall)
			}
		}
	}

	// 9. Skill and instructions.
	if in.SkillFS == nil {
		add("Skill lodan-memoria", LevelWarn, "el binario no lleva la skill embebida", reinstall)
	} else if ok, detail := CheckSkill(in.SkillFS, in.Env); ok {
		add("Skill lodan-memoria", LevelOK, detail, "")
	} else {
		add("Skill lodan-memoria", LevelWarn, detail, reinstall)
	}
	if ok, detail := CheckInstructions(in.Env); ok {
		add("Instrucciones globales", LevelOK, detail, "")
	} else {
		add("Instrucciones globales", LevelWarn, detail, reinstall)
	}

	nErrors, nWarnings := 0, 0
	for _, r := range results {
		switch r.Level {
		case LevelError:
			nErrors++
		case LevelWarn:
			nWarnings++
		}
	}
	in.printf("\n%d comprobaciones: %d error(es), %d aviso(s).\n", len(results), nErrors, nWarnings)
	return results
}

// checkService reports the state of a system service and returns whether it is
// registered. primary is the lodan service (a stopped one is an error); the
// Ollama one only warns.
func (in *Installer) checkService(name string, primary bool, add func(string, Level, string, string)) bool {
	label := "Servicio " + name
	st, err := in.manager().Status(name)
	switch {
	case err != nil:
		add(label, LevelWarn, "no se pudo consultar: "+err.Error(), in.adminCmd("lodan service status --name "+name))
		return false
	case st.State == service.StateNotInstalled:
		if primary {
			add(label, LevelWarn, "no está registrado: PostgreSQL arranca solo cuando un cliente lanza «lodan serve»", "ejecuta «lodan install» (sin --skip-service)")
		} else {
			add(label, LevelWarn, "no está registrado", "ejecuta «lodan install»")
		}
		return false
	case st.State == service.StateStopped:
		level := LevelWarn
		if primary {
			level = LevelError
		}
		add(label, level, "registrado pero parado ("+st.Detail+")", in.adminCmd("lodan service start --name "+name))
	case st.State != service.StateRunning:
		add(label, LevelWarn, "estado «"+string(st.State)+"» ("+st.Detail+")", in.adminCmd("lodan service restart --name "+name))
	case !st.Enabled:
		add(label, LevelWarn, "en marcha pero no arranca con el sistema", in.adminCmd("lodan service enable --name "+name))
	default:
		add(label, LevelOK, "en marcha y habilitado", "")
	}
	return true
}

// checkDatabase checks that PostgreSQL runs, accepts the connection and has a
// supported pgvector.
func (in *Installer) checkDatabase(ctx context.Context, cl clusterAPI, serviceInstalled bool, add func(string, Level, string, string)) {
	const name = "Base de datos"
	cctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()

	running, err := cl.Status(cctx)
	switch {
	case err != nil:
		add(name, LevelError, "no se pudo consultar el estado de PostgreSQL: "+err.Error(), "revisa los logs en "+in.Cfg.LogsDir())
		return
	case !running && !serviceInstalled:
		add(name, LevelWarn, "PostgreSQL no está en marcha: arranca cuando un cliente lanza «lodan serve»", "ejecuta «lodan db start» para arrancarlo ahora")
		return
	case !running:
		add(name, LevelError, "PostgreSQL no está en marcha", in.adminCmd("lodan service restart")+" y revisa "+filepath.Join(in.Cfg.LogsDir(), "postgres.log"))
		return
	}

	version, err := cl.VectorVersion(cctx)
	switch {
	case err != nil:
		add(name, LevelError, "no se pudo comprobar la conexión y pgvector: "+err.Error(), "ejecuta «lodan migrate» y revisa "+filepath.Join(in.Cfg.LogsDir(), "postgres.log"))
	case !versionAtLeast(version, minPgvector):
		add(name, LevelError, "pgvector "+version+" es anterior a "+minPgvector, reinstallHint())
	default:
		add(name, LevelOK, "conexión correcta; pgvector "+version, "")
	}
}

func reinstallHint() string {
	return "borra el entorno runtime/pg y ejecuta «lodan install»"
}

// checkOllama checks the server, the model and a test embedding.
func (in *Installer) checkOllama(ctx context.Context, add func(string, Level, string, string)) {
	api := in.ollama()
	url, model := in.Cfg.OllamaURL, in.Cfg.EmbedModel

	version, up := api.Detect(ctx, url)
	if !up {
		fix := "arranca Ollama («ollama serve») o corrige ollama_url en " + in.Cfg.ConfigFile()
		if _, err := api.Find(in.ollamaInstallDir()); err == nil {
			fix = in.adminCmd("lodan service start --name " + OllamaServiceName)
		}
		add("Ollama", LevelError, "no responde en "+url, fix)
		add("Modelo "+model, LevelWarn, "no comprobado: Ollama no responde", "")
		add("Embedding de prueba", LevelWarn, "no comprobado: Ollama no responde", "")
		return
	}
	add("Ollama", LevelOK, "versión "+version+" en "+url, "")

	has, err := api.HasModel(ctx, url, model)
	switch {
	case err != nil:
		add("Modelo "+model, LevelError, err.Error(), "revisa Ollama")
		add("Embedding de prueba", LevelWarn, "no comprobado: no se pudo listar los modelos", "")
		return
	case !has:
		add("Modelo "+model, LevelError, "no está descargado en Ollama", "ejecuta «ollama pull "+model+"» o «lodan install»")
		add("Embedding de prueba", LevelWarn, "no comprobado: falta el modelo", "")
		return
	}
	add("Modelo "+model, LevelOK, "disponible en Ollama", "")

	if err := api.Embed(ctx, in.Cfg); err != nil {
		add("Embedding de prueba", LevelError, err.Error(), "comprueba que embed_dims ("+fmt.Sprint(in.Cfg.EmbedDims)+") corresponde al modelo "+model)
	} else {
		add("Embedding de prueba", LevelOK, fmt.Sprintf("vector de %d dimensiones", in.Cfg.EmbedDims), "")
	}
}
