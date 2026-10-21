package memory

import (
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
