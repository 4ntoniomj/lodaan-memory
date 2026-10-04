# Verificación 002 — Instalador

Fecha: 2026-10-04 · Rama: `feat/instalador` · Máquina: i5-4590, 4 núcleos, 8 GB, sin GPU dedicada para cálculo (Radeon RX550 vía Vulkan en Windows); Windows 11 con WSL2

## Resultado global

`gofmt -l .` vacío. `go vet ./...` limpio con `GOOS` linux, windows y darwin. `go test ./...` en verde. Compilación cruzada correcta para las 5 plataformas.

- **Linux:** instalación real como servicio systemd desde el 2026-09-29 (`lodan status`: `BD: ok`).
- **Windows 11, 2026-10-03:** prueba sin elevar lanzada desde WSL: `install --yes --skip-service`, `doctor`, MCP, supervisor en primer plano, backup y restore, `uninstall --purge`.
- **Windows 11, 2026-10-04:** instalación completa con UAC desde PowerShell: lodan descarga Ollama y registra los servicios `lodan` y `lodan-ollama` en el SCM, que quedan en marcha con arranque automático. Después: reinstalación con los servicios en marcha, `doctor` con 12 comprobaciones, 0 errores y 0 avisos, y MCP `remember`/`recall` contra los servicios.

## Criterios de aceptación

| # | Estado | Evidencia |
|---|---|---|
| 1 | ✅ | Linux y Windows: micromamba con SHA-256, PostgreSQL + pgvector (Windows: PostgreSQL 16.15 y pgvector 0.8.6), clúster, migraciones y `BD: ok` / `doctor` sin errores |
| 2 | ✅ | Windows: con un Ollama en 127.0.0.1:11434 se reutiliza y no se descarga otro; Linux: Ollama del sistema reutilizado |
| 3 | ✅ | Windows: lodan descarga Ollama 0.35.1 en `%LOCALAPPDATA%\lodan\runtime\ollama` y lo registra como servicio `lodan-ollama`, que escucha solo en 127.0.0.1. En la primera instalación la espera de 60 s venció mientras Ollama detectaba la GPU (67 s) y el modelo se descargó con `/api/pull` a mano. La espera ampliada a 3 min (`e7ad23d`) no se ha vuelto a probar desde cero |
| 4 | ✅ | Windows: Gemini CLI detectado, entrada añadida con copia `.bak-lodan` y retirada al desinstalar sin tocar el resto; reinstalar no duplica nada |
| 5 | ✅ | Windows y Linux: skill en `~/.claude/skills` y bloque en las instrucciones globales; retirada de `lodan-memory` en tests |
| 6 | ⚠️ Parcial | Linux: servicio systemd real gestionado con `systemctl` y `lodan service`. Windows: `lodan` y `lodan-ollama` registrados en el SCM tras el UAC, en marcha y con arranque automático; `lodan service status` funciona sin elevar. Falta en Windows: `lodan service stop/start/restart` como administrador y el reinicio del equipo. macOS: solo en unidad |
| 7 | ✅ | Windows: `doctor` con 12 comprobaciones (incluidos ambos servicios) y solución propuesta en cada aviso |
| 8 | ⚠️ Parcial | Windows sin servicios (2026-10-03): `uninstall --purge` quita la entrada del cliente, los bloques, la skill y las carpetas vacías que creó lodan, para PostgreSQL y borra los datos tras escribir «borrar». Con servicios registrados no se ha probado: necesita el UAC |
| 9 | ✅ | Solo el paso de servicios pide administrador (UAC); los datos van a `%LOCALAPPDATA%\lodan`; los servicios corren como LocalSystem. Todo escucha en 127.0.0.1 |
| 10 | ✅ | `go test ./...`; macOS compilado y probado solo en unidad, como prevé el criterio |

## Fallos encontrados en la prueba real de Windows (corregidos)

| Commit | Fallo |
|---|---|
| `acfdad9` | La suma de micromamba se pedía como `micromamba-win-64.exe.sha256`, que no existe (404) |
| `e122594` | `pg_ctl start` acababa con `exec.ErrWaitDelay` porque el postmaster hereda la tubería de salida |
| `0c097e5` | `doctor` y `lodan service status` exigían administrador para consultar el SCM |
| `0b4c51c` | El instalador creaba `~/.claude` al instalar la skill y `doctor` daba por instalado Claude Code |
| `6bf36eb` | `lodan-ollama` se registraba con `ollama.exe` directamente, que no habla el protocolo del SCM (error 1053) ni entiende `--env`; ahora lo envuelve `lodan service ollama`. El error del paso con administrador se perdía en su ventana: ahora queda en `logs/service-elevated.log` y el instalador lo muestra |
| `3fe522e` | Sin elevar, `pg_ctl status` no ve el PostgreSQL del servicio (LocalSystem) y lo daba por parado: install, `doctor` y `lodan serve` intentaban arrancar otro. Ahora se confirma con una conexión autenticada |
| `e7ad23d` | Reinstalar con los servicios en marcha no podía sobrescribir el binario en uso (ahora se aparta a `.old` y se avisa de reiniciar); la espera a Ollama pasa de 60 s a 3 min |

## Pendiente con administrador

Ejecutar en PowerShell como administrador y comprobar que los servicios se reinician con el binario nuevo, se paran y arrancan, y que un backup pausa el servicio y lo vuelve a arrancar:

```
$L = "C:\Users\4ntoniomj\AppData\Local\lodan\bin\lodan.exe"
& $L service restart
& $L service restart --name lodan-ollama
& $L service stop
& $L service status
& $L service start
& $L service status
& $L db backup --to C:\Users\4ntoniomj\lodan-prueba\bk
& $L service status
```

Después, desde PowerShell normal: `C:\Users\4ntoniomj\lodan-prueba\lodan.exe uninstall --purge` (UAC y escribir «borrar»), y comprobar que no quedan servicios (`sc.exe query lodan`, `sc.exe query lodan-ollama`) ni `%LOCALAPPDATA%\lodan`.

## Huecos

| Severidad | Hueco | Acción |
|---|---|---|
| Media | Windows: sin probar `lodan service stop/start/restart` como administrador, el backup con el servicio, el reinicio del equipo y `uninstall --purge` con servicios | Ejecutar el bloque de «Pendiente con administrador» |
| Media | Windows: un backup lanzado sin elevar con el servicio en marcha no puede parar el PostgreSQL de LocalSystem y falla con un error | Ejecutarlo como administrador; documentarlo en el README |
| Baja | Desde WSL, la elevación con UAC se cancela sin mostrar la ventana («El usuario ha cancelado la operación») | Instalar y desinstalar desde una terminal de Windows |
| Baja | lodan en WSL y en Windows a la vez se disputan 127.0.0.1:54329 y :11434 a través del relé de localhost de WSL: el que arranca primero se queda el puerto | Documentar; opcional: puerto distinto en una de las dos instalaciones |
| Baja | El paso 4 del instalador presenta al Ollama del servicio `lodan-ollama` como uno externo («se reutiliza») | Reconocer el servicio propio en el mensaje |
| Baja | En Windows cada `pg_ctl start` tarda 5 s más (vence `WaitDelay` antes de liberar la tubería) | Redirigir la salida de `pg_ctl start` a un archivo en lugar de una tubería |
| Info | macOS solo compilado y probado en unidad | Previsto por el criterio 10 |
