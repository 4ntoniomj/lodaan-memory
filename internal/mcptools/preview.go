package mcptools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lodan/internal/memory"
	"lodan/internal/recall"
)

// previewTitle is the first line of a remember preview.
const previewTitle = "VISTA PREVIA — no se ha guardado nada"

// preview answers remember with preview: true: every check of a real save,
// without writing anything.
func (s *Server) preview(ctx context.Context, items []memory.Item) (*mcp.CallToolResult, any, error) {
	pvs, err := s.d.Memory.Preview(ctx, items)
	if err != nil {
		return errorResult(err), nil, nil
	}
	lines := []string{previewTitle}
	for i, pv := range pvs {
		lines = append(lines, fmt.Sprintf("ítem %d «%s»", i+1, oneLine(pv.Title)))
		lines = append(lines, "  etiquetas: "+formatTags(pv.Tags))
		if !s.d.ConcreteTagOptional && !memory.HasConcreteTag(pv.Tags) {
			lines = append(lines, "  falta una etiqueta concreta (proyecto-, persona-, contacto-, proveedor-, cliente-, finca-, vehiculo-, coche-, skill-, herramienta-, recurso-, evento-)")
		}
		if _, tRep := memory.RepairMojibake(items[i].Title); tRep {
			lines = append(lines, "  texto reparado (codificación)")
		} else if _, cRep := memory.RepairMojibake(items[i].Content); cRep {
			lines = append(lines, "  texto reparado (codificación)")
		}
		if pv.DuplicateOf > 0 {
			lines = append(lines, fmt.Sprintf("  ya existe: #%d (mismo contenido)", pv.DuplicateOf))
		}
		if pv.KeyHolder > 0 {
			lines = append(lines, fmt.Sprintf("  clave %s: sustituiría a #%d", pv.Key, pv.KeyHolder))
		}
		for _, sg := range pv.Suggestions {
			lines = append(lines, fmt.Sprintf("  ¿alias?: #%s ≈ #%s (%d registros)", sg.New, sg.Existing, sg.Records))
		}

		var tagIDs []int32
		var slugs []string
		for _, r := range pv.Tags {
			slugs = append(slugs, r.Topic.Slug)
			if r.Topic.ID != 0 {
				tagIDs = append(tagIDs, r.Topic.ID)
			}
		}
		if pv.DuplicateOf == 0 {
			if near, found, _ := memory.FindNearDuplicate(ctx, s.d.Pool, items[i].Content, items[i].Key, tagIDs); found {
				if near.Sim >= 0.90 {
					lines = append(lines, fmt.Sprintf("  casi idéntico a #%d (parecido %.2f)", near.ID, near.Sim))
				} else if near.Sim >= 0.75 && near.SharedTag {
					lines = append(lines, fmt.Sprintf("  ¿duplicado?: #%d (parecido %.2f)", near.ID, near.Sim))
				}
			}
			prior := make([]string, i)
			for j := range prior {
				prior[j] = items[j].Content
			}
			if bnear, found, _ := memory.FindNearInBatch(ctx, s.d.Pool, items[i].Content, prior); found {
				if bnear.Sim >= 0.90 {
					lines = append(lines, fmt.Sprintf("  casi idéntico al item %d (parecido %.2f)", bnear.Index+1, bnear.Sim))
				} else if bnear.Sim >= 0.75 && sharesTag(pv.Tags, pvs[bnear.Index].Tags) {
					lines = append(lines, fmt.Sprintf("  ¿duplicado?: item %d (parecido %.2f)", bnear.Index+1, bnear.Sim))
				}
			}
		}
		links, err := s.d.Recall.ConnectedByTags(ctx, tagIDs, maxKeyLinks)
		if err != nil {
			links = nil
		}
		if pv.Key == "" {
			if key, id, ok, err := s.d.Recall.SuggestKey(ctx, links, slugs, pv.Title); err == nil && ok {
				lines = append(lines, fmt.Sprintf("  clave sugerida: %s (sustituiría a #%d)", key, id))
			}
		}
		if len(links) > 0 {
			lines = append(lines, "  conectaría: "+formatLinks(links[:min(len(links), maxLinks)]))
		}

		cands, err := s.d.Recall.Candidates(ctx, recall.CandidateRequest{
			Text:     items[i].Title + " " + items[i].Content,
			Vector:   pv.Vector,
			TopicIDs: tagIDs,
			Exclude:  []int64{},
			Limit:    candidateLimit,
		})
		if err == nil {
			for _, c := range cands {
				lines = append(lines, "  revisa: "+c.Line())
			}
		}
	}
	return textResult(strings.Join(lines, "\n")), nil, nil
}
