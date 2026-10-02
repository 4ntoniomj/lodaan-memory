# CLAUDE.md — service

## Resumen

**Problema**: lodan debe arrancar con el equipo y gestionarse con las herramientas nativas de cada sistema, sin que la IA ni el usuario toquen `systemctl`, `launchctl` o el SCM a mano.
**Objetivo**: el supervisor `lodan service run` (PostgreSQL en primer plano y tareas de fondo) y la interfaz `Manager` con una implementación por SO que registra, controla y consulta el servicio del sistema (`Install`, `Uninstall`, `Start`, `Stop`, `Restart`, `Enable`, `Disable`, `Status`).
**Alcance**: dentro: supervisor, unidad de systemd, LaunchDaemon y servicio del SCM. Fuera: instalar PostgreSQL u Ollama y configurar clientes MCP (`internal/install`); la elevación con `sudo`/UAC (la hace quien llama).

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial. |
| `usar-git` | Cualquier commit o rama que la toque. |

## Convenciones específicas

- Un archivo por SO con build tag: `manager_linux.go`, `manager_darwin.go`, `manager_windows.go`, `manager_other.go`. `manager_unix.go` (`!windows`) contiene los sustitutos de `IsWindowsService` y `RunAsWindowsService`. `runner.go` (`linux || darwin`) tiene la interfaz `runner` que sustituye a `systemctl` y `launchctl` en los tests.
- Las funciones que generan archivos (`renderUnit`, `renderPlist`) son puras y se prueban sin tocar el sistema. Los gestores de Linux y macOS se prueban con un `runner` falso y un directorio temporal; nunca ejecutan comandos reales en los tests.
- Todos los tiempos de parada suman lo mismo: el supervisor espera 30 s a PostgreSQL; systemd (`TimeoutStopSec=45`), launchd (`ExitTimeOut` 45) y el SCM (espera de `Stop` de 45 s) dejan margen. `KillMode=mixed` en systemd.
- macOS: `Stop` y `Restart` usan `launchctl bootout` (y `bootstrap` en `Start`), no `kill` ni `kickstart -k`, porque `KeepAlive` relanzaría el proceso. El plist se queda en `/Library/LaunchDaemons`, así que un servicio parado arranca en el siguiente reinicio salvo que esté deshabilitado.
- Windows: el SCM no tiene entorno por servicio, así que el entorno viaja como argumentos `--env K=V` que `lodan service run` debe aceptar. La cuenta del servicio (`Spec.User`) solo puede ser una sin contraseña (LocalSystem, LocalService, NetworkService o `NT SERVICE\<nombre>`). PostgreSQL rechaza cuentas de administrador: la cuenta definitiva se decide con la prueba real.
- Windows y Ollama: `ollama.exe` no habla con el SCM (error 1053 al arrancar). En Windows `lodan-ollama` se registra con `lodan service ollama --ollama-exe <ruta> --log <archivo>` (subcomando interno, `cmd/lodan/service_ollama.go`), que contesta al SCM con `RunAsWindowsService` y lanza `ollama serve` como hijo con `RunChild` (`child.go`): su salida va al log, si el hijo muere solo devuelve error (el SCM aplica la recuperación) y al parar mata al hijo y a sus descendientes con `taskkill /T /F` (Ollama lanza procesos runner; `Process.Kill` los dejaría huérfanos). En Unix el hijo recibe SIGINT y, pasados `childStopTimeout` (30 s), se mata. systemd y launchd siguen ejecutando ollama directamente. `Install` ya es idempotente en Windows: si el servicio existe, `updateService` actualiza ruta, argumentos, tipo de inicio y recuperación.
- Los errores incluyen la salida del comando que falló.
- Si PostgreSQL se para con el lock del clúster tomado (backup o restore), el supervisor lo trata como pausa de mantenimiento (`errMaintenancePause`): espera a que se libere el lock y vuelve a preparar PostgreSQL. Mientras vive, toca `supervisor.heartbeat` cada 5 s para que backup y restore sepan que hay supervisor.

## Pruebas

`go test ./internal/service/...`. Las de otros SO se comprueban con compilación cruzada: `GOOS=darwin go vet ./internal/service/` y `GOOS=windows go vet ./internal/service/`.
