package memory

import (
	"bytes"
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NearMatch describe un registro vigente con contenido muy parecido.
type NearMatch struct {
	ID        int64
	Sim       float64
	SharedTag bool
}

// FindNearDuplicate busca el registro activo más parecido al contenido dado
// mediante la similitud trigram (pg_trgm).
// Si excludeKey no está vacía, se ignora el registro vigente con esa misma clave
// (ya que este ítem lo va a sustituir por key).
// Se excluyen duplicados exactos (mismo content_hash) para que la deduplicación
// exacta de Remember opere con normalidad.
// Devuelve found=true si hay algún registro vigente con similitud >= 0.70.
func FindNearDuplicate(ctx context.Context, pool *pgxpool.Pool, content string, excludeKey string, topicIDs []int32) (NearMatch, bool, error) {
	if pool == nil {
		return NearMatch{}, false, nil
	}
	if topicIDs == nil {
		topicIDs = []int32{}
	}
	hash := ContentHash(content)
	var m NearMatch
	query := `
		SELECT m.id, similarity(lower(m.content), lower($1)) AS sim,
		       EXISTS(SELECT 1 FROM memory_topics mt WHERE mt.memory_id = m.id AND mt.topic_id = ANY($3::int[])) AS shared_tag
		FROM memories m
		WHERE m.status = 'active'
		  AND ($2 = '' OR m.key IS NULL OR m.key <> $2)
		  AND m.content_hash <> $4
		  AND similarity(lower(m.content), lower($1)) >= 0.70
		ORDER BY sim DESC
		LIMIT 1
	`
	err := pool.QueryRow(ctx, query, content, excludeKey, topicIDs, hash).Scan(&m.ID, &m.Sim, &m.SharedTag)
	if err != nil {
		return NearMatch{}, false, nil
	}
	return m, true, nil
}

// BatchNearMatch describe un ítem anterior de la misma llamada con contenido muy parecido.
type BatchNearMatch struct {
	// Index es la posición del ítem en prior.
	Index int
	Sim   float64
}

// FindNearInBatch busca, entre los contenidos de los ítems anteriores de la misma
// llamada (prior), el más parecido al contenido dado mediante la similitud
// trigram (pg_trgm). FindNearDuplicate solo ve los registros ya guardados, así que
// sin esta comprobación dos casi duplicados de un mismo lote se guardarían los dos.
// Se omiten los de contenido idéntico: la deduplicación exacta de Remember los trata.
// Devuelve found=true si alguno llega a 0.70.
func FindNearInBatch(ctx context.Context, pool *pgxpool.Pool, content string, prior []string) (BatchNearMatch, bool, error) {
	if pool == nil || len(prior) == 0 {
		return BatchNearMatch{}, false, nil
	}
	hash := ContentHash(content)
	var texts []string
	var index []int
	for i, p := range prior {
		if !bytes.Equal(ContentHash(p), hash) {
			texts = append(texts, p)
			index = append(index, i)
		}
	}
	if len(texts) == 0 {
		return BatchNearMatch{}, false, nil
	}
	var ord int
	var sim float64
	err := pool.QueryRow(ctx, `SELECT t.ord, similarity(lower(t.c), lower($1)) AS sim
		FROM unnest($2::text[]) WITH ORDINALITY AS t(c, ord)
		ORDER BY sim DESC, t.ord
		LIMIT 1`, content, texts).Scan(&ord, &sim)
	if err != nil {
		return BatchNearMatch{}, false, err
	}
	if sim < 0.70 {
		return BatchNearMatch{}, false, nil
	}
	return BatchNearMatch{Index: index[ord-1], Sim: sim}, true, nil
}
