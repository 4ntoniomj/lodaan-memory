// migrate-taxonomy reorganiza las etiquetas concretas del data dir activo para
// que matcheen la taxonomía de D2 (12 familias, jerarquía con `/`).
//
// Es idempotente: si se corre dos veces, la segunda no hace nada útil. Trabaja
// contra el data dir apuntado por LODAN_DATA_DIR (o el default del OS).
//
// Lo que hace:
//  1. Lee el whitelist hardcoded (tag → familia) y crea los topics canónicos
//     que falten.
//  2. Crea aliases confirmed (alias → canonical) para los tags del whitelist.
//  3. Para cada record que llevaba un tag viejo, asocia el topic canónico
//     correspondiente y desactiva el viejo.
//  4. Renombra el prefijo de las `key` que apuntaban a tags viejos.
//  5. Confirma los pares singular/plural de D2 §6.1 que aún estén "suggested".
//
// Tras correr, imprime métricas: tags concretas por familia, records con tag
// concreta, huérfanos, pares de aliases confirmados.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/config"
)

// whitelist mapea tags actuales a su familia canónica según D2 §3.
// Cada entrada es (tag_actual, familia, slug).
//   - tag_actual: el slug que existe hoy en la BD.
//   - familia: una de las 12 de D2 §3.
//   - slug: el slug del topic canónico (sin la familia como prefijo).
type mapping struct {
	familia string
	slug    string
}

// whitelist es la fuente de verdad del mapeo. Cualquier tag aquí se renombra.
var whitelist = []mapping{
	// recurso: cosas que respaldan el trabajo diario
	{"recurso", "gasoil"},
	{"recurso", "partidas"},
	{"recurso", "cajas-palets"},
	{"recurso", "pedidos"},
	{"recurso", "automatizacion"},
	{"recurso", "registro-trabajadores"},
	{"recurso", "lame"},          // sistema LAME exportacion
	{"recurso", "lame-exportacion"}, // subsistema
	{"recurso", "pendrive"},
	{"recurso", "opencode"},
	{"recurso", "backend"},
	{"recurso", "go"},            // tecnología
	{"recurso", "windows"},
	{"recurso", "powershell"},
	{"recurso", "testing"},
	{"recurso", "finca-encargado"},
	{"recurso", "carteles"},
	{"recurso", "transporte"},
	{"recurso", "glosario"},
	{"recurso", "impresion"},
	{"recurso", "logistica"},
	{"recurso", "red"},
	{"recurso", "albaranes"},
	{"recurso", "migracion-go"},
	{"recurso", "saldos"},
	{"recurso", "drive"},
	{"recurso", "dias-a-mano"},
	{"recurso", "docker"},
	{"recurso", "catalogo"},
	{"recurso", "spec-009"},

	// herramienta: software
	{"herramienta", "excel"},
	{"herramienta", "vbscript"},
	{"herramienta", "gentle-ai"},
	{"herramienta", "claude-code"},
	{"herramienta", "antigravity"},
	{"herramienta", "sin-ollama"},

	// proveedor: empresas que venden
	{"proveedor", "florette"},
	{"proveedor", "bonduelle"},
	{"proveedor", "endesa"},
	{"proveedor", "iberdrola"},
	{"proveedor", "verdysalad"},   // alias de vds
	{"proveedor", "vegetales-de-murcia"}, // alias de vdm
	{"proveedor", "dieselplus"},

	// evento: hitos / operaciones
	{"evento", "cosecha"},
	{"evento", "expediciones"},
	{"evento", "entrenamiento"},

	// cliente: empresas a las que vendemos
	{"cliente", "mercadona"},
	{"cliente", "agridemur-marketing"},
	{"cliente", "el-prado-de-jumilla"}, // alias de prado
	{"cliente", "hortalizas-frescas-sureste"}, // alias de hfs
	{"cliente", "linea-verde"},                // alias de vlv
	{"cliente", "marketing"},
	{"cliente", "agritecsur"},                 // alias de ats

	// proyecto: con repo o worktree
	{"proyecto", "erp-agm"},
	{"proyecto", "agm-cosechas"},
	{"proyecto", "lodan-memory"},
	{"proyecto", "lodaan-memory"},
	{"proyecto", "erp"},

	// contacto: personas profesionales
	// (vacío por ahora — añadir según se identifiquen)

	// vehiculo: matrículas sin guiones (la familia existe, sin datos aún)

	// skill: skills instaladas o que se aprenden
	{"skill", "lodan-memoria"},
	{"skill", "lodaan-memory"}, // singularización del repo

	// proyecto: con repo o worktree
	{"proyecto", "erp-agm"},
	{"proyecto", "agm-cosechas"},
	{"proyecto", "lodan-memory"},
	{"proyecto", "lodaan-memory"},

	// persona, finca, recurso-evento quedan con sus formas compuestas
}

// aliasesSingularPlural son los pares de D2 §6.1 que aún estén en
// `state='suggested'` los promovemos a `confirmed`.
var aliasesSingularPlural = []struct{ alias, canonical string }{
	{"cosechas", "cosecha"},
	{"contactos", "contacto"},
	{"vehiculos", "vehiculo"},
	{"fincas", "finca"},
	{"clientes", "cliente"},
	{"proveedores", "proveedor"},
	{"personas", "persona"},
	{"proyectos", "proyecto"},
	{"skills", "skill"},
	{"herramientas", "herramienta"},
	{"eventos", "evento"},
	{"recursos", "recurso"},
}

// slugCanónico devuelve "<familia>-<slug>".
func (m mapping) canónico() string { return m.familia + "-" + m.slug }

func main() {
	dryRun := flag.Bool("dry-run", false, "no escribe nada; solo muestra qué haría")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("data dir: %s", cfg.DataDir)

	ctx := context.Background()
	pool, err := openPool(ctx, cfg)
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	whitelistByTag := make(map[string]mapping, len(whitelist))
	for _, m := range whitelist {
		whitelistByTag[m.slug] = m
	}

	if err := run(ctx, pool, whitelistByTag, *dryRun); err != nil {
		log.Fatalf("migrate: %v", err)
	}
}

// openPool construye un pool contra el data dir de cfg. Usa el secret que el
// propio binario lodan escribe al instalar.
func openPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	if cfg.PGDataDir() == "" {
		return nil, errors.New("PGDataDir vacío")
	}
	secret, err := os.ReadFile(cfg.SecretFile())
	if err != nil {
		return nil, fmt.Errorf("leer secret: %w", err)
	}
	pw := strings.TrimSpace(string(secret))

	conn, err := pgxpool.ParseConfig(fmt.Sprintf(
		"host=127.0.0.1 port=%d user=lodan password=%s dbname=lodan sslmode=disable",
		cfg.PGPort, pw,
	))
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

func run(ctx context.Context, pool *pgxpool.Pool, whitelisted map[string]mapping, dryRun bool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Fase 1 — topics canónicos (crea los que falten).
	canonicalIDs := make(map[string]int32, len(whitelisted))
	for _, m := range whitelisted {
		can := m.canónico()
		id, err := upsertTopic(ctx, tx, can)
		if err != nil {
			return fmt.Errorf("upsertTopic(%s): %w", can, err)
		}
		canonicalIDs[can] = id
	}

	// Fase 2 — aliases confirmed para los tags viejos.
	for _, m := range whitelisted {
		canID := canonicalIDs[m.canónico()]
		if err := upsertAlias(ctx, tx, m.slug, m.canónico(), canID, "confirmed", dryRun); err != nil {
			return fmt.Errorf("upsertAlias(%s): %w", m.slug, err)
		}
	}

	// Fase 3 — asocia records al topic canónico y desactiva el viejo.
	// Para cada tag viejo: busca los records que lo llevan, busca el canónico,
	// crea memory_topics si no existe, marca el viejo como active=false.
	for _, m := range whitelisted {
		if err := rewireRecords(ctx, tx, m.slug, m.canónico(), dryRun); err != nil {
			return fmt.Errorf("rewireRecords(%s): %w", m.slug, err)
		}
	}

	// Fase 4 — renombra keys que apuntaban al tag viejo.
	for _, m := range whitelisted {
		if err := rewriteKey(ctx, tx, m.slug, m.canónico(), dryRun); err != nil {
			return fmt.Errorf("rewriteKey(%s): %w", m.slug, err)
		}
	}

	// Fase 5 — confirma aliases singular/plural sugeridos.
	for _, p := range aliasesSingularPlural {
		canID, err := lookupTopicID(ctx, tx, p.canonical)
		if err != nil {
			return fmt.Errorf("lookupTopic(%s): %w", p.canonical, err)
		}
		if canID == 0 {
			continue // el canónico no existe aún, lo dejaremos para otro round
		}
		if err := upsertAlias(ctx, tx, p.alias, p.canonical, canID, "confirmed", dryRun); err != nil {
			return fmt.Errorf("upsertAlias(%s): %w", p.alias, err)
		}
	}

	if dryRun {
		log.Printf("DRY-RUN: rollback intencional")
		return tx.Rollback(ctx)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return report(ctx, pool)
}

func upsertTopic(ctx context.Context, tx pgx.Tx, slug string) (int32, error) {
	var id int32
	err := tx.QueryRow(ctx, `
		INSERT INTO topics (slug) VALUES ($1)
		ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
		RETURNING id`, slug).Scan(&id)
	return id, err
}

func upsertAlias(ctx context.Context, tx pgx.Tx, alias, canonical string, canonicalID int32, state string, dryRun bool) error {
	if dryRun {
		log.Printf("alias %q → %q (%s, id=%d) [dry-run]", alias, canonical, state, canonicalID)
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO tag_aliases (alias, canonical, state)
		VALUES ($1, $2, $3)
		ON CONFLICT (alias, canonical) DO UPDATE SET state = EXCLUDED.state`,
		alias, canonical, state)
	return err
}

// rewireRecords: para cada memory_topics del tag viejo en records activos,
// crea la asociación al canónico (si no existe) y desactiva el viejo.
func rewireRecords(ctx context.Context, tx pgx.Tx, oldTag, canónico string, dryRun bool) error {
	oldID, err := lookupTopicID(ctx, tx, oldTag)
	if err != nil {
		return err
	}
	if oldID == 0 {
		return nil
	}
	canID, err := lookupTopicID(ctx, tx, canónico)
	if err != nil {
		return err
	}

	// Itera los memory_topics viejos activos: crea el canónico (si no existe),
	// desactiva el viejo. Sin defer: cada llamada a tx abre una conexión y un
	// defer en un loop agotaría las conexiones disponibles.
	rows, err := tx.Query(ctx, `
		SELECT mt.memory_id
		FROM memory_topics mt
		WHERE mt.topic_id = $1 AND mt.active = true`, oldID)
	if err != nil {
		return err
	}
	var memIDs []int64
	for rows.Next() {
		var memID int64
		if err := rows.Scan(&memID); err != nil {
			rows.Close()
			return err
		}
		memIDs = append(memIDs, memID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, memID := range memIDs {
		// Asegura que existe el canónico para este record.
		if _, err := tx.Exec(ctx, `
			INSERT INTO memory_topics (topic_id, memory_id, kind, active, ts)
			SELECT $1, $2, m.kind, true, m.created_at
			FROM memories m WHERE m.id = $3
			ON CONFLICT (topic_id, memory_id) DO NOTHING`,
			canID, memID, memID); err != nil {
			return err
		}
		// Desactiva el viejo.
		if _, err := tx.Exec(ctx, `
			UPDATE memory_topics SET active = false
			WHERE topic_id = $1 AND memory_id = $2 AND active = true`,
			oldID, memID); err != nil {
			return err
		}
	}
	if dryRun || len(memIDs) > 0 {
		log.Printf("rewire %s → %s: %d records", oldTag, canónico, len(memIDs))
	}
	return nil
}

// rewriteKey: si la key empieza con "oldTag/", renombra el prefijo a
// "<familia>-<slug>/" usando la forma canónica.
func rewriteKey(ctx context.Context, tx pgx.Tx, oldTag, canónico string, dryRun bool) error {
	prefix := oldTag + "/"
	newPrefix := canónico + "/"
	res, err := tx.Exec(ctx, `
		UPDATE memories SET key = replace(key, $1, $2)
		WHERE key LIKE $1 || '%' AND status = 'active'`,
		prefix, newPrefix)
	if err != nil {
		return err
	}
	if dryRun {
		log.Printf("rewrite key prefix %q → %q (no aplicado, dry-run)", prefix, newPrefix)
		return nil
	}
	if n := res.RowsAffected(); n > 0 {
		log.Printf("rewrite key %q → %q: %d records", prefix, newPrefix, n)
	}
	return nil
}

func lookupTopicID(ctx context.Context, tx pgx.Tx, slug string) (int32, error) {
	var id int32
	err := tx.QueryRow(ctx, `SELECT id FROM topics WHERE slug = $1`, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// report imprime las métricas post-migración.
func report(ctx context.Context, pool *pgxpool.Pool) error {
	var total, withConcrete, huérfanos int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM memories WHERE status = 'active'),
			(SELECT count(DISTINCT m.id) FROM memories m
			 JOIN memory_topics mt ON mt.memory_id = m.id AND mt.active
			 JOIN topics t ON t.id = mt.topic_id
			 WHERE m.status = 'active'
			   AND t.slug ~ '^(proyecto|persona|contacto|proveedor|cliente|finca|vehiculo|coche|skill|herramienta|recurso|evento)-[a-z0-9]'),
			(SELECT count(*) FROM memories m
			 WHERE status = 'active'
			   AND NOT EXISTS (SELECT 1 FROM memory_topics mt
			                   JOIN topics t ON t.id = mt.topic_id
			                   WHERE mt.memory_id = m.id AND mt.active
			                     AND t.slug ~ '^(proyecto|persona|contacto|proveedor|cliente|finca|vehiculo|coche|skill|herramienta|recurso|evento)-[a-z0-9]'))
	`).Scan(&total, &withConcrete, &huérfanos); err != nil {
		return err
	}

	var confirmed, suggested int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM tag_aliases WHERE state = 'confirmed'),
			(SELECT count(*) FROM tag_aliases WHERE state = 'suggested')`).Scan(&confirmed, &suggested); err != nil {
		return err
	}

	pct := func(a, b int64) string {
		if b == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f%%", float64(a)/float64(b)*100)
	}

	fmt.Println("---- métricas post-migración ----")
	fmt.Printf("records vigentes:           %d\n", total)
	fmt.Printf("records con tag concreta:   %d (%s)\n", withConcrete, pct(withConcrete, total))
	fmt.Printf("records huérfanos:          %d (%s)\n", huérfanos, pct(huérfanos, total))
	fmt.Printf("aliases confirmados:        %d\n", confirmed)
	fmt.Printf("aliases sugeridos:          %d\n", suggested)
	return nil
}