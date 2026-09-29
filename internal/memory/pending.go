package memory

import (
	"context"
	"fmt"

	"lodan/internal/embedding"
)

// FillPending computes and stores the embeddings of up to limit records whose
// embedding is NULL, in batches of 32 (one short transaction per batch, with
// FOR UPDATE SKIP LOCKED so several processes can run it at once). It returns
// how many records were filled. If the embedder fails, it returns the count so
// far together with the error (wrapping embedding.ErrUnavailable when Ollama
// is down).
func (s *Service) FillPending(ctx context.Context, limit int) (int, error) {
	filled := 0
	for filled < limit {
		n, err := s.fillBatch(ctx, min(fillBatchSize, limit-filled))
		filled += n
		if err != nil {
			return filled, err
		}
		if n == 0 {
			break
		}
	}
	return filled, nil
}

// fillBatch fills up to batch pending records in one transaction and returns
// how many were filled.
func (s *Service) fillBatch(ctx context.Context, batch int) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("no se pudo iniciar la transacción de embeddings pendientes: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op tras un Commit correcto

	rows, err := tx.Query(ctx, `SELECT id, title, content FROM memories
		WHERE embedding IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, batch)
	if err != nil {
		return 0, fmt.Errorf("no se pudieron leer los registros pendientes: %w", err)
	}
	var ids []int64
	var docs []embedding.Document
	for rows.Next() {
		var id int64
		var d embedding.Document
		if err := rows.Scan(&id, &d.Title, &d.Text); err != nil {
			rows.Close()
			return 0, fmt.Errorf("no se pudo leer un registro pendiente: %w", err)
		}
		ids = append(ids, id)
		docs = append(docs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("no se pudieron leer los registros pendientes: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	vecs, err := s.emb.EmbedDocuments(ctx, docs)
	if err != nil {
		return 0, fmt.Errorf("no se pudieron calcular los embeddings pendientes: %w", err)
	}
	if len(vecs) != len(ids) {
		return 0, fmt.Errorf("el embedder devolvió %d vectores para %d registros", len(vecs), len(ids))
	}
	modelID, err := s.embeddingModelID(ctx)
	if err != nil {
		return 0, err
	}

	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE memories SET embedding = $2, embedding_model = $3 WHERE id = $1`,
			id, halfParam(vecs[i]), modelID); err != nil {
			return 0, fmt.Errorf("no se pudo guardar el embedding del registro #%d: %w", id, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("no se pudieron confirmar los embeddings pendientes: %w", err)
	}
	return len(ids), nil
}
