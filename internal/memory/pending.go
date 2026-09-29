package memory

import (
	"context"
	"fmt"

	"lodan/internal/embedding"
)

// filled is a pending record that has just received its embedding.
type filled struct {
	id  int64
	vec []float32
}

// FillPending computes and stores the embeddings of up to limit records whose
// embedding is NULL, in batches of 32 (one short transaction per batch, with
// FOR UPDATE SKIP LOCKED so several processes can run it at once). It returns
// how many records were filled. If the embedder fails, it returns the count so
// far together with the error (wrapping embedding.ErrUnavailable when Ollama
// is down).
//
// Once a batch is committed, every record of it that is still active goes
// through the same similar-record detection as Remember: suggested "related"
// relations to active records at or above the duplicate threshold. That step is
// best effort and never changes the returned count or error.
func (s *Service) FillPending(ctx context.Context, limit int) (int, error) {
	total := 0
	for total < limit {
		n, done, err := s.fillBatch(ctx, min(fillBatchSize, limit-total))
		total += n
		for _, f := range done {
			s.suggestSimilar(ctx, f.id, f.vec, []int64{f.id})
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			break
		}
	}
	return total, nil
}

// fillBatch fills up to batch pending records in one transaction and returns
// how many were filled, plus the ones that are active (the only ones that get
// suggested relations) with their vectors. The list is only returned after the
// commit, so the new embeddings are visible to the similarity search.
func (s *Service) fillBatch(ctx context.Context, batch int) (int, []filled, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("no se pudo iniciar la transacción de embeddings pendientes: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op tras un Commit correcto

	rows, err := tx.Query(ctx, `SELECT id, title, content, status = 'active' FROM memories
		WHERE embedding IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, batch)
	if err != nil {
		return 0, nil, fmt.Errorf("no se pudieron leer los registros pendientes: %w", err)
	}
	var ids []int64
	var docs []embedding.Document
	var active []bool
	for rows.Next() {
		var id int64
		var d embedding.Document
		var isActive bool
		if err := rows.Scan(&id, &d.Title, &d.Text, &isActive); err != nil {
			rows.Close()
			return 0, nil, fmt.Errorf("no se pudo leer un registro pendiente: %w", err)
		}
		ids = append(ids, id)
		docs = append(docs, d)
		active = append(active, isActive)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("no se pudieron leer los registros pendientes: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil, nil
	}

	vecs, err := s.emb.EmbedDocuments(ctx, docs)
	if err != nil {
		return 0, nil, fmt.Errorf("no se pudieron calcular los embeddings pendientes: %w", err)
	}
	if len(vecs) != len(ids) {
		return 0, nil, fmt.Errorf("el embedder devolvió %d vectores para %d registros", len(vecs), len(ids))
	}
	modelID, err := s.embeddingModelID(ctx)
	if err != nil {
		return 0, nil, err
	}

	var done []filled
	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE memories SET embedding = $2, embedding_model = $3 WHERE id = $1`,
			id, halfParam(vecs[i]), modelID); err != nil {
			return 0, nil, fmt.Errorf("no se pudo guardar el embedding del registro #%d: %w", id, err)
		}
		if active[i] {
			done = append(done, filled{id: id, vec: vecs[i]})
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("no se pudieron confirmar los embeddings pendientes: %w", err)
	}
	return len(ids), done, nil
}
