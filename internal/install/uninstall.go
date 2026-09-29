package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lodan/internal/service"
)

// purgeWord is what the user must type to confirm --purge. Not even --yes skips it.
const purgeWord = "borrar"

// UninstallOptions are the flags of `lodan uninstall`.
type UninstallOptions struct {
	// Yes skips the general confirmation (never the one of Purge).
	Yes bool
	// DryRun describes every action without doing it.
	DryRun bool
	// Purge also deletes the whole data directory, after typing "borrar".
	Purge bool
}

// Uninstall removes, in order: the client entries, the instruction blocks, the
// skill, the system services, the symlink and the stable binary. The data
// (pg/, runtime/, config.json, secret) stays unless Purge is set.
func (in *Installer) Uninstall(ctx context.Context, o UninstallOptions) error {
	in.reset(Options{Yes: o.Yes, DryRun: o.DryRun})

	title := "lodan: desinstalación"
	if o.DryRun {
		title += " (simulación: no se borra ni se ejecuta nada)"
	}
	in.printf("%s\nDatos en %s\n", title, in.Cfg.DataDir)

	switch {
	case o.Purge:
		if err := checkPurgeTarget(in.Cfg.DataDir, in.Env.Home); err != nil {
			return err
		}
		in.printf("\nATENCIÓN: --purge borrará %s entero: la base de datos con todos los recuerdos, la configuración, el Ollama de lodan y sus modelos.\n", in.Cfg.DataDir)
		if !o.DryRun {
			in.printf("  ? Para confirmarlo escribe «%s»: ", purgeWord)
			if !strings.EqualFold(in.readLine(), purgeWord) {
				in.printf("  No se ha confirmado el borrado: no se ha cambiado nada.\n")
				return ErrCancelled
			}
		}
	case !o.DryRun:
		if !in.confirm("¿Desinstalar lodan (clientes, skill, servicios y binario)? Tus datos se conservan.") {
			in.printf("  No se ha cambiado nada.\n")
			return ErrCancelled
		}
	}

	steps := []struct {
		title string
		run   func(context.Context) error
	}{
		{"Clientes MCP", in.removeClients},
		{"Instrucciones globales", in.removeInstructions},
		{"Skill", in.removeSkill},
		{"Servicios del sistema", in.removeServices},
		{"Enlace y binario", in.removeBinary},
		{"Datos", func(ctx context.Context) error { return in.removeData(ctx, o.Purge) }},
	}
	for i, s := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		in.printf("\n[%d/%d] %s\n", i+1, len(steps), s.title)
		if err := s.run(ctx); err != nil {
			in.fail("%v", err)
		}
	}

	in.printf("\nResumen\n")
	switch {
	case o.DryRun:
		in.printf("  … [simulación] no se ha cambiado nada; quita --dry-run para desinstalar de verdad.\n")
	case in.failures > 0:
		in.printf("  ✗ Desinstalación con %d error(es) y %d aviso(s).\n", in.failures, in.warnings)
	case in.warnings > 0:
		in.printf("  ✓ Desinstalación completada con %d aviso(s).\n", in.warnings)
	default:
		in.printf("  ✓ Desinstalación completada.\n")
	}
	if !o.Purge {
		in.printf("  Tus datos siguen en %s; las copias .bak-lodan de los archivos de los clientes también se conservan.\n", in.Cfg.DataDir)
	}
	if in.failures > 0 {
		return fmt.Errorf("la desinstalación terminó con %d error(es)", in.failures)
	}
	return nil
}

// checkPurgeTarget refuses to delete a directory that is not clearly a lodan
// data directory: the root, the home (or one of its parents), or a folder
// without any lodan file.
func checkPurgeTarget(dataDir, home string) error {
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return fmt.Errorf("el directorio de datos %q no es una ruta absoluta: no se borra", dataDir)
	}
	clean := filepath.Clean(dataDir)
	if clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return fmt.Errorf("el directorio de datos %q es la raíz del disco: no se borra", dataDir)
	}
	if home != "" {
		if rel, err := filepath.Rel(clean, filepath.Clean(home)); err == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("el directorio de datos %q contiene tu carpeta personal: no se borra", dataDir)
		}
	}
	if !pathExists(clean) {
		return nil // nothing to delete
	}
	for _, marker := range []string{"config.json", "secret", "pg", "runtime", "bin"} {
		if pathExists(filepath.Join(clean, marker)) {
			return nil
		}
	}
	return fmt.Errorf("%q no parece un directorio de datos de lodan (no contiene config.json, pg, runtime ni bin): no se borra", dataDir)
}

// removeClients removes the lodan entry from every client that has one.
func (in *Installer) removeClients(ctx context.Context) error {
	changedCount, problems := 0, 0
	for _, c := range buildClients(in.Env) {
		if c.File() == "" {
			continue
		}
		changed, err := Unconfigure(c, in.opt.DryRun)
		switch {
		case err != nil && IsWarning(err):
			problems++
			in.warn("%s: %v", c.Name, err)
		case err != nil:
			problems++
			in.fail("%s: %v", c.Name, err)
		case changed && in.opt.DryRun:
			changedCount++
			in.would("%s: se quitaría la entrada lodan de %s", c.Name, c.File())
		case changed:
			changedCount++
			in.ok("%s: entrada lodan quitada de %s", c.Name, c.File())
		}
	}
	if changedCount == 0 && problems == 0 {
		in.ok("ningún cliente tenía la entrada de lodan (ya estaba)")
	}
	return nil
}

func (in *Installer) removeInstructions(ctx context.Context) error {
	lines, err := UninstallInstructions(in.Env, in.opt.DryRun)
	for _, l := range lines {
		in.reportLine(l)
	}
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		in.ok("no había bloques de lodan en las instrucciones globales (ya estaba)")
	}
	return nil
}

func (in *Installer) removeSkill(ctx context.Context) error {
	lines, err := UninstallSkill(in.Env, in.opt.DryRun)
	for _, l := range lines {
		in.reportLine(l)
	}
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		in.ok("la skill lodan-memoria no estaba instalada (ya estaba)")
	}
	return nil
}

// removeServices unregisters lodan and lodan-ollama through the elevated
// `lodan service uninstall --ollama`.
func (in *Installer) removeServices(ctx context.Context) error {
	names := []string{service.DefaultName, OllamaServiceName}
	var present []string
	for _, n := range names {
		st, err := in.manager().Status(n)
		if err == nil && st.State == service.StateNotInstalled {
			continue
		}
		// A status that cannot be read counts as "maybe installed": trying is harmless.
		present = append(present, n)
	}
	if len(present) == 0 {
		in.ok("no había servicios registrados (ya estaba)")
		return nil
	}

	bin := in.bin
	if !isRegularFile(bin) {
		exe, err := in.executable()
		if err != nil {
			return fmt.Errorf("no se encuentra un lodan con el que quitar los servicios: %w", err)
		}
		bin = exe
	}
	args := ServiceUninstallArgs(true)
	how := "sudo"
	if in.Env.GOOS == "windows" {
		how = "UAC"
	}
	if in.opt.DryRun {
		in.would("se pararían y quitarían los servicios %s con permisos de administrador (%s): %s %s",
			strings.Join(present, ", "), how, bin, strings.Join(args, " "))
		return nil
	}

	in.doing("quitando los servicios %s: hacen falta permisos de administrador (%s)", strings.Join(present, ", "), how)
	if err := in.elevate()(ctx, bin, args); err != nil {
		return fmt.Errorf("no se pudieron quitar los servicios: %w", err)
	}
	for _, n := range present {
		st, err := in.manager().Status(n)
		switch {
		case err != nil:
			in.warn("servicio %s: no se pudo verificar que se haya quitado: %v", n, err)
		case st.State == service.StateNotInstalled:
			in.ok("servicio %s quitado", n)
		default:
			in.warn("el servicio %s sigue registrado (estado «%s»)", n, st.State)
		}
	}
	return nil
}

// removeBinary removes the symlink in ~/.local/bin (only if it points to the
// stable binary) and the stable binary itself.
func (in *Installer) removeBinary(ctx context.Context) error {
	if in.Env.GOOS != "windows" && in.Env.Home != "" {
		link := filepath.Join(in.Env.Home, ".local", "bin", "lodan")
		if target, err := os.Readlink(link); err == nil && target == in.bin {
			switch {
			case in.opt.DryRun:
				in.would("se quitaría el enlace %s", link)
			default:
				if err := os.Remove(link); err != nil {
					in.warn("no se pudo quitar el enlace %s: %v", link, err)
				} else {
					in.ok("enlace quitado: %s", link)
				}
			}
		}
	}

	if _, err := os.Lstat(in.bin); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			in.ok("el binario %s ya no estaba", in.bin)
			return nil
		}
		return err
	}
	if in.opt.DryRun {
		in.would("se borraría el binario %s", in.bin)
		return nil
	}
	if err := os.Remove(in.bin); err != nil {
		in.warn("no se pudo borrar %s: %v (si está en uso, ciérralo y bórralo a mano)", in.bin, err)
		return nil
	}
	in.ok("binario borrado: %s", in.bin)
	// Removes bin/ only if it is empty.
	_ = os.Remove(filepath.Dir(in.bin))
	return nil
}

// removeData keeps the data, or with purge stops PostgreSQL and deletes the
// whole data directory.
func (in *Installer) removeData(ctx context.Context, purge bool) error {
	dir := in.Cfg.DataDir
	if !purge {
		in.ok("datos conservados en %s (pg/, runtime/, config.json y secret); --purge los borra", dir)
		return nil
	}
	if in.opt.DryRun {
		in.would("se pararía PostgreSQL y se borraría %s entero", dir)
		return nil
	}

	if cl, err := in.newCluster(in.Cfg); err == nil {
		if running, err := cl.Status(ctx); err == nil && running {
			in.doing("parando PostgreSQL antes de borrar los datos")
			if err := cl.Stop(ctx); err != nil {
				return fmt.Errorf("no se pudo parar PostgreSQL: %w", err)
			}
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("no se pudo borrar %s: %w", dir, err)
	}
	in.ok("datos borrados: %s", dir)
	return nil
}
