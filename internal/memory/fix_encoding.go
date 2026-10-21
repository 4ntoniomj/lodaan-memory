package memory

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
)

// MalformedRecord contiene el id y título original de un registro con mojibake.
type MalformedRecord struct {
	ID    int64
	Title string
}

// CollisionRecord describe un registro cuya reparación colisionaría con otro registro activo.
type CollisionRecord struct {
	ID      int64
	OtherID int64
}

// FixEncodingResult describe el resultado de la simulación o ejecución de fix-encoding.
type FixEncodingResult struct {
	Malformed  []MalformedRecord
	Collisions []CollisionRecord
	Repaired   int
}

// FixEncoding revisa todos los registros vigentes en busca de texto mal codificado (mojibake).
// Si apply es false, se ejecuta como simulación y solo detecta los registros afectados.
// Si apply es true, repara título y contenido a través de la misma ruta que revise update,
// omitiendo aquellos cuyo contenido reparado colisionaría con el hash de otro registro activo.
func (s *Service) FixEncoding(ctx context.Context, apply bool) (FixEncodingResult, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, title, content FROM memories WHERE status = 'active' ORDER BY id`)
	if err != nil {
		return FixEncodingResult{}, fmt.Errorf("no se pudieron consultar los registros vigentes: %w", err)
	}
	defer rows.Close()

	type itemToFix struct {
		id                   int64
		title, content       string
		newTitle, newContent string
	}
	var toFix []itemToFix

	for rows.Next() {
		var id int64
		var title, content string
		if err := rows.Scan(&id, &title, &content); err != nil {
			return FixEncodingResult{}, fmt.Errorf("error al leer registro: %w", err)
		}
		newTitle, titleRepaired := RepairMojibake(title)
		newContent, contentRepaired := RepairMojibake(content)
		if titleRepaired || contentRepaired {
			toFix = append(toFix, itemToFix{
				id:         id,
				title:      title,
				content:    content,
				newTitle:   newTitle,
				newContent: newContent,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return FixEncodingResult{}, fmt.Errorf("error al iterar registros: %w", err)
	}

	var res FixEncodingResult
	for _, it := range toFix {
		res.Malformed = append(res.Malformed, MalformedRecord{ID: it.id, Title: it.title})
	}

	if !apply {
		return res, nil
	}

	for _, it := range toFix {
		// Si el contenido cambió, verificamos colisión con otro registro activo
		if it.newContent != it.content {
			hash := ContentHash(it.newContent)
			var otherID int64
			err := s.pool.QueryRow(ctx, `SELECT id FROM memories WHERE content_hash = $1 AND status = 'active' AND id <> $2`, hash, it.id).Scan(&otherID)
			if err == nil {
				res.Collisions = append(res.Collisions, CollisionRecord{ID: it.id, OtherID: otherID})
				continue
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return res, fmt.Errorf("error al comprobar colisión de hash para #%d: %w", it.id, err)
			}
		}

		// Reutilizamos Revise update para recalcular content_hash, embedding, tsv y updated_at
		_, err := s.Revise(ctx, nil, Revision{
			Action:  "update",
			ID:      it.id,
			Title:   it.newTitle,
			Content: it.newContent,
		})
		if err != nil {
			return res, fmt.Errorf("error al actualizar #%d: %w", it.id, err)
		}
		res.Repaired++
	}

	return res, nil
}

// Format renderiza la salida de fix-encoding según el formato exacto del contrato.
func (res FixEncodingResult) Format(out io.Writer, apply bool) {
	if !apply {
		for _, m := range res.Malformed {
			fmt.Fprintf(out, "#%d %s\n", m.ID, m.Title)
		}
		if len(res.Malformed) == 0 {
			fmt.Fprintln(out, "0 registros con texto mal codificado")
		} else {
			fmt.Fprintf(out, "%d registros con texto mal codificado (simulación: no se ha cambiado nada; usa --apply)\n", len(res.Malformed))
		}
		return
	}

	for _, c := range res.Collisions {
		fmt.Fprintf(out, "#%d omitido: quedaría duplicado de #%d\n", c.ID, c.OtherID)
	}
	fmt.Fprintf(out, "%d registros reparados\n", res.Repaired)
}
