# Verificación 003 — Backup seguro

Fecha: 2026-10-02 · Rama: `feat/003-backup-seguro` · Máquina: i5-4590, 4 núcleos, 8 GB, sin GPU (WSL2)

## Resultado global

`gofmt -l .` vacío. `go vet ./...` limpio, también con `GOOS=windows` y `GOOS=darwin` en los paquetes tocados. `go test ./...` en verde contra PostgreSQL 18.6 + pgvector reales, sin tests saltados en `internal/database` ni en `internal/service`. Prueba de extremo a extremo con el binario, `lodan service run` y Ollama reales en un `LODAN_DATA_DIR` y un HOME temporales (puerto 54399): correcta.

## Criterios de aceptación

| Criterio | Estado | Evidencia |
|---|---|---|
| Backup full por defecto con nombre `lodan_backup_AAAA-MM-DD_HHMMSS.full.tar.zst` | ✅ | E2E y `TestBackupFullIdaYVuelta` |
| `--incremental`: cambios desde el último backup de cualquier tipo | ✅ | `TestBackupCadenaConSemanticaEstandar` (Since de cada backup) y E2E |
| `--differential`: cambios desde el último full | ✅ | `TestBackupCadenaConSemanticaEstandar` y E2E |
| Restore desde un full | ✅ | `TestBackupFullIdaYVuelta`, E2E con PostgreSQL parado |
| Restore de una carpeta con full + diff + incr, en orden | ✅ | E2E: vuelven los datos A, B y C, y desaparece el posterior a los backups |
| Destino por defecto: directorio temporal del sistema | ✅ | `lodan db backup` (flag `--to`) |
| El archivo contiene info.txt, manifest.txt, pg/, config.json (si existe) y secret | ✅ | E2E con `tar --zstd -tf` |
| Archivos borrados entre backups no reaparecen | ✅ | `TestBackupBorradosEntreBackupsNoResucitan` (DROP TABLE y VACUUM FULL) |
| Cadena incompleta o inconsistente: error sin tocar el clúster | ✅ | `TestRestoreCadenasInvalidas`, `TestRestoreManifiestoInconsistenteNoTocaElClusterActual`; E2E con un incremental truncado |
| lodan sigue funcionando tras backup y restore con el servicio en marcha | ✅ | `TestRunSupervisorSobreviveABackupYRestore`; E2E: el supervisor mantiene su PID y PostgreSQL vuelve como hijo suyo |

## Mediciones (E2E, clúster de 42 MB)

- Backup full: 0,55 s en total, 4,1 MB comprimido. Pausa de PostgreSQL con el servicio en marcha: ~1 s.
- Restore de full + diff + incr: 0,56 s.
- Compresión sobre 472 MB del clúster real: xz 2,1 MB/s (descartado), zstd 75 MB/s.

## Huecos

| Severidad | Hueco | Acción |
|---|---|---|
| Baja | El test de WAL reciclado no llegó a provocar un segmento renombrado con mtime antiguo; la regla queda cubierta por `TestCollectChangedFilesUsaElManifiestoDeReferencia` | Ninguna por ahora |
| Baja | Con PostgreSQL vigilado por sondeo (variante `pg_ctl` o externo), una pausa puede detectarse tarde y registrarse como fallo; el servicio se reinicia solo | Documentado en CODEBASE.md |
| Baja | Un lock de destino huérfano (backup matado con kill -9) bloquea el siguiente backup hasta 2 minutos | El mensaje de error indica qué archivo borrar |
| Info | Los backups del formato 1 (xz, sin manifiesto) no se pueden restaurar | Aceptado: la funcionalidad no llegó a publicarse |
| Info | La parada del backup crece con lo que se copia (velocidad de disco); con el clúster real actual (4,4 GB, de los que 3,4 GB son `lodan_bench`) serían decenas de segundos | `lodan_bench` se borrará del clúster real (decisión del usuario) |
