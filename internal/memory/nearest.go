package memory

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"lodan/internal/database"
)

const (
	// minEfSearch is the lowest hnsw.ef_search used by Nearest.
	minEfSearch = 40
	// maxEfSearch is the highest value pgvector accepts for hnsw.ef_search.
	maxEfSearch = 1000
)

// EfSearch returns the hnsw.ef_search that Nearest sets for a lookup of the given
// number of candidates: the candidates themselves, but at least 40 and at most 1000
// (the highest value pgvector accepts).
func EfSearch(candidates int) int {
	return min(max(candidates, minEfSearch), maxEfSearch)
}

// CandidatesSQL returns the statement of the first step of Nearest: it looks up
// candidate ids in the binary-quantized HNSW index (Hamming distance).
//
// Parameters: $1 is the query vector (halfvec) and $2 the number of candidates.
// dims is the dimension of the embeddings column and must be a positive integer:
// it is written into the SQL text, not bound as a parameter.
//
// This is the single source of truth of the query: `lodan bench` measures exactly
// this text, so any change here must be validated with `lodan bench`.
func CandidatesSQL(dims int) string {
	d := strconv.Itoa(dims)
	return `SELECT id FROM memories
		WHERE status = 'active' AND embedding IS NOT NULL
		ORDER BY binary_quantize(embedding)::bit(` + d + `) <~> binary_quantize($1::halfvec(` + d + `))::bit(` + d + `)
		LIMIT $2`
}

// RerankSQL returns the statement of the second step of Nearest: it reorders the
// candidates with the full-precision vectors and returns (id, title, similarity).
//
// Parameters: $1 the candidate ids (bigint[]), $2 the query vector (halfvec), $3
// the ids to leave out (bigint[], never NULL) and $4 the maximum number of rows.
// dims has the same meaning as in CandidatesSQL.
//
// Like CandidatesSQL it is measured by `lodan bench`: validate any change with it.
func RerankSQL(dims int) string {
	d := strconv.Itoa(dims)
	return `SELECT id, title, 1 - (embedding <=> $2::halfvec(` + d + `)) AS sim
		FROM memories
		WHERE id = ANY($1) AND NOT (id = ANY($3)) AND embedding IS NOT NULL
		ORDER BY embedding <=> $2::halfvec(` + d + `)
		LIMIT $4`
}

// Nearest returns up to limit active records whose embedding has a cosine
// similarity of at least minSim to vec, most similar first, leaving out excludeIDs.
//
// It looks up `candidates` records in the binary-quantized HNSW index (Hamming
// distance) and reorders them with the full-precision vectors. dims is the
// dimension of the embeddings column.
//
// hnsw.ef_search is set with SET LOCAL semantics, which needs a transaction, so
// q may be:
//   - a *pgxpool.Pool: Nearest opens (and closes) its own read-only transaction;
//   - a pgx.Tx: the caller's transaction is used as is, and hnsw.ef_search stays
//     set until that transaction ends.
//
// Any other Querier is rejected.
func Nearest(ctx context.Context, q database.Querier, vec []float32, dims, candidates, limit int, minSim float64, excludeIDs []int64) ([]Neighbor, error) {
	if dims <= 0 {
		return nil, fmt.Errorf("dimensión de embeddings no válida: %d", dims)
	}
	if len(vec) != dims {
		return nil, fmt.Errorf("el vector tiene %d dimensiones y se esperaban %d", len(vec), dims)
	}
	if limit <= 0 {
		return nil, nil
	}
	if candidates < limit {
		candidates = limit
	}

	switch c := q.(type) {
	case *pgxpool.Pool:
		tx, err := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			return nil, fmt.Errorf("no se pudo iniciar la transacción de búsqueda: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }() // solo lectura: no hay nada que confirmar
		return nearestIn(ctx, tx, vec, dims, candidates, limit, minSim, excludeIDs)
	case pgx.Tx:
		return nearestIn(ctx, c, vec, dims, candidates, limit, minSim, excludeIDs)
	default:
		return nil, fmt.Errorf("la búsqueda necesita un *pgxpool.Pool o una pgx.Tx, no %T", q)
	}
}

// nearestIn runs the two-step search inside a transaction.
func nearestIn(ctx context.Context, tx database.Querier, vec []float32, dims, candidates, limit int, minSim float64, excludeIDs []int64) ([]Neighbor, error) {
	ef := EfSearch(candidates)
	// set_config(..., true) equals SET LOCAL but accepts parameters.
	if _, err := tx.Exec(ctx, `SELECT set_config('hnsw.ef_search', $1, true)`, strconv.Itoa(ef)); err != nil {
		return nil, fmt.Errorf("no se pudo ajustar hnsw.ef_search: %w", err)
	}

	param := pgvector.NewHalfVector(vec)

	rows, err := tx.Query(ctx, CandidatesSQL(dims), param, candidates)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron buscar los candidatos semánticos: %w", err)
	}
	ids := make([]int64, 0, candidates)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("no se pudo leer un candidato: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron buscar los candidatos semánticos: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	if excludeIDs == nil {
		excludeIDs = []int64{} // un NULL en NOT (id = ANY(NULL)) descartaría todas las filas
	}
	rows, err = tx.Query(ctx, RerankSQL(dims), ids, param, excludeIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron reordenar los candidatos: %w", err)
	}
	defer rows.Close()

	var out []Neighbor
	for rows.Next() {
		var n Neighbor
		if err := rows.Scan(&n.ID, &n.Title, &n.Similarity); err != nil {
			return nil, fmt.Errorf("no se pudo leer un vecino: %w", err)
		}
		if n.Similarity >= minSim {
			out = append(out, n)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no se pudieron reordenar los candidatos: %w", err)
	}
	return out, nil
}
