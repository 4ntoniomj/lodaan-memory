[4antoniomj:integracion] Eres la INTEGRACIÓN de un trabajo en paralelo: no lo dividas ni escribas prompts de parte; une las partes ya construidas.

# Integración — lodan-auditoria

Pega este prompt en un chat nuevo cuando las dos partes hayan entregado (criterios S#, RED/GREEN, sello y QA).

## Rol

Eres el integrador: unes las 2 partes (A busqueda, B escritura) en `feat/lodan-auditoria`. Cada parte solo podía tocar sus archivos y la sección de `README.md` que le correspondía; tú compruebas que respetaron el contrato y resuelves los conflictos (gana la versión del contrato).

## Contexto

- Proyecto: lodan lite (Go 1.25, PostgreSQL, MCP).
- Directorio de trabajo: `C:\Users\4ntoniomj\repos\lodaan-memory` (carpeta principal, rama `feat/lodan-auditoria`; ejecuta todo aquí).
- Contrato vigente: `C:\Users\4ntoniomj\repos\lodaan-memory\docs\paralelo\lodan-auditoria\contrato.md`
- Ramas y worktrees:
  - `feat/lodan-auditoria-busqueda` → `C:\Users\4ntoniomj\repos\lodaan-memory.worktrees\busqueda`
  - `feat/lodan-auditoria-escritura` → `C:\Users\4ntoniomj\repos\lodaan-memory.worktrees\escritura`
- Orden de merge: primero B (escritura: migración 0006 y datos), después A (busqueda: migración 0005 y lectura). Las migraciones se aplican por número (0005 → 0006) sea cual sea el orden de merge.
- Suite completa: `go test ./...` · lint `go vet ./...` · build `go build ./...`. Fallos previos conocidos en Windows: `TestCopyToStaging` y `TestEnsureMicromambaExecutable`; no cuentan como regresión si también fallan en `adf2f9d`.
- En la carpeta principal hay archivos sin seguimiento ajenos a este trabajo (`tmp/`, `dev/`, `check_utf8_main.go`): no los borres ni los commitees.
- Skills a cargar: `tdd-qa`: `C:\Users\4ntoniomj\.claude\skills\tdd-qa\SKILL.md` · `work-unit-commits`: `C:\Users\4ntoniomj\.claude\skills\work-unit-commits\SKILL.md`

## Pasos

1. Para cada rama, `git diff --name-only feat/lodan-auditoria...feat/lodan-auditoria-<parte>`: si tocó un archivo que no posee según la sección 2 del contrato, es un error de separación; se revierte a favor del contrato y se anota.
2. Mergea en orden con `git merge --no-ff`. Conflicto esperado: `README.md` (se conservan los dos textos). Cualquier otro es un error de corte y se anota.
3. Verifica que cada parte respetó el contrato: campo `mode` y `force`, mensajes exactos, migraciones 0005 y 0006, fixtures. Cualquier desvío es un bug.
4. Corre la suite completa una vez, con migraciones aplicadas desde cero sobre un directorio de datos en `mktemp -d`.
5. QA de costuras (`tdd-qa`, puede usar el subagente `4antoniomj-qa`), con un directorio de datos de prueba y nunca el activo:
   - Guardar con `remember` un texto con mojibake (se repara) y encontrarlo después con `recall` sin tildes (`albaran`).
   - Guardar un casi duplicado con `force: true` y comprobar que `recall mode="list"` muestra los dos en orden de fecha.
   - `lodan db fix-encoding --apply` sobre registros con mojibake y después `recall query="medicion"` los encuentra (la columna `tsv` con `lodan_es` se recalcula al actualizar).
   - Rendimiento: `go run ./cmd/lodan bench` antes (en `adf2f9d`) y después; informa de cualquier empeoramiento mayor del 20 %.
6. Vuelve al objetivo del contrato: ¿se cumple de punta a punta? Si no, di exactamente qué falta.
7. Con todo verde: elimina cada worktree con `git worktree remove <ruta>` y cada rama mergeada con `git branch -d`. Si un worktree tiene archivos sin seguimiento o modificados, `git worktree remove` falla: lista esos archivos y pregunta al usuario antes de usar `--force`. Nunca `git branch -D`.
8. Después del veredicto, **pregunta al usuario** (una pregunta, y espera) si quiere el despliegue. Solo con su sí: `lodan db backup --full`; parar el servicio; compilar e instalar en `C:\Users\4ntoniomj\bin\lodan.lite.exe`; arrancar (aplica 0005 y 0006); `lodan db fix-encoding` en simulación, mostrarle la lista y, con un segundo sí, `--apply`. Esto modifica la base de datos real: nunca sin las dos confirmaciones.

## Reglas

- Push, PR y merge a la rama principal son decisión del usuario.
- Conventional Commits, sin atribución de IA ni `Co-Authored-By`.

## Entrega

1. Partes integradas y orden
2. Tabla S# → pass/fail por parte
3. Conflictos y resolución
4. Resultado de la suite completa
5. Hallazgos del QA de costuras (o «0 hallazgos»; NO PROBADO explícito) y comparación de `lodan bench`
6. Veredicto sobre el objetivo: cumple / no cumple y por qué
7. Worktrees eliminados o conservados, y por qué
8. Línea `Risk:` (como mínimo item 1, migración y reparación de datos, e item 3, contrato de las herramientas MCP)
