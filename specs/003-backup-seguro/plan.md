# Plan: Backup y restauración segura de lodan

## Enfoque técnico

Tres componentes:

1. **Backup**: Método `Cluster.Backup(ctx, destDir, backupType)` en cluster.go que:
   - Adquiere lock, pausa PostgreSQL
   - Según tipo (full/incr/diff), copia /pg/ (completo o solo cambios)
   - Comprime en tar.xz (máxima compresión)
   - Retorna ruta: `/destino/lodan_backup_YYYY-MM-DD_HHMMSS.<tipo>.tar.xz`

2. **Restore**: Método `Cluster.Restore(ctx, files []string)` en cluster.go que:
   - Adquiere lock, pausa PostgreSQL
   - Valida que primero viene full backup
   - Extrae en orden: full → incr → diff
   - Reinicia PostgreSQL
   - Verifica restauración

3. **CLI**: Actualizar `db.go`:
   - `lodan db backup [--to] [--full|--incremental|--differential]` → delega a Backup()
   - `lodan db restore --from archivo1 [archivo2 ...]` o `lodan db restore --from /carpeta` → delega a Restore()

## Componentes / archivos afectados

- `internal/database/cluster.go` — `Backup(ctx, destDir, backupType)`, `Restore(ctx, files)`, helpers para tar.xz, detección de cambios (incr/diff)
- `cmd/lodan/db.go` — Cases "backup" y "restore" en runDB(), parsing de flags --to, --full, --incremental, --differential, --from
- `cmd/lodan/main.go` — Actualizar usageText para incluir backup y restore
- README.md — Documentar backup/restore con ejemplos (vía skill crear-readme)

## Stack y dependencias nuevas

- `archive/tar` — estándar en Go, para crear tar
- `github.com/klauspost/compress/zstd` (v1.20.1, ya era dependencia del módulo) — compresión zstd

**Decisión** (2026-10-02): zstd. Se descartó xz (`github.com/ulikunitz/xz`) tras medirlo sobre 472 MB del clúster real: xz comprimía a 2,1 MB/s (246 MB de salida) y zstd a 75 MB/s (267 MB, un 8 % más). Con xz un full paraba PostgreSQL minutos.

## Alternativas consideradas

- **Script bash standalone**: descartada por riesgo de inconsistencia.
- **Copiar directorios en lugar de tar**: descartada porque tar comprimido es más portable y compacto.
- **Backup diferencial a nivel de bloques (rsync)**: descartada porque complejidad innecesaria; full+incr+diff es suficiente.
- **Detectar cambios sin manifiesto (solo mtime)**: descartada. Con solo mtime, los archivos que PostgreSQL borra entre backups (DROP TABLE, VACUUM FULL, segmentos de WAL) reaparecen al restaurar. Se usa mtime para elegir qué copiar y un manifiesto para saber qué debe existir.

## Semántica de los tipos y manifiesto

- **full**: todo pg/, config.json y secret.
- **diferencial** (`--differential`, `.diff.tar.xz`): archivos de pg/ modificados desde el último full del destino.
- **incremental** (`--incremental`, `.incr.tar.xz`): archivos de pg/ modificados desde el último backup de cualquier tipo del destino.
- Todos llevan `manifest.txt`: lista completa de directorios y archivos de pg/ en el momento del backup, con tamaños. Al restaurar se extrae la cadena a un directorio temporal, se borran los archivos que no estén en el manifiesto del último backup y se comprueba que estén todos los del manifiesto con su tamaño; si no, se aborta antes de sustituir el clúster.
- Formato `lodan-backup/2` (el 1 no tenía manifiesto y no se acepta: la funcionalidad no llegó a publicarse).
- **Copia previa**: con PostgreSQL parado solo se copian las entradas a respaldar a `<destino>/.lodan_backup_<fecha>.<tipo>.staging/`; después se rearranca PostgreSQL, se suelta el lock del clúster (el supervisor del servicio lo necesita para arrancar PostgreSQL) y se comprime desde el staging. El staging se borra siempre; los huérfanos de un backup interrumpido se limpian al empezar el siguiente.

## Riesgos

- **Downtime durante backup/restore**: la parada del backup dura lo que la copia al staging (velocidad de disco); la del restore, lo que tarda en extraer la cadena. *Mitigación*: copia previa y zstd.
- **Espacio en disco**: el destino necesita espacio para la copia sin comprimir de lo respaldado más el archivo final. *Mitigación*: el staging se borra siempre; un error por falta de espacio aborta sin dejar restos.
- **Restore fallido a mitad**: Datos pueden quedar inconsistentes. *Mitigación*: usar defer para garantizar reinicio PostgreSQL; documentar que restore es operación atómica (parada-completa o rollback).
- **Permisos de archivo**: tar.xz preserva permisos; verificar en tests.
- **Detectar cambios en incr/diff**: por mtime con un margen de 2 s. *Mitigación*: el manifiesto con tamaños detecta al restaurar una cadena incompleta o inconsistente.
