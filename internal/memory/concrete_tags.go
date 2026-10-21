package memory

import (
	"regexp"

	"lodan/internal/topic"
)

var concreteTagRe = regexp.MustCompile(`^(proyecto|persona|contacto|proveedor|cliente|finca|vehiculo|coche|skill|herramienta|recurso|evento)-[a-z0-9]`)

// HasConcreteTag comprueba si al menos una de las etiquetas resueltas pertenece
// a una de las 12 familias de etiquetas concretas (D2 §5).
func HasConcreteTag(resolutions []topic.Resolution) bool {
	for _, res := range resolutions {
		slug := res.Topic.Slug
		if slug == "" {
			slug = res.Requested
		}
		if concreteTagRe.MatchString(slug) {
			return true
		}
	}
	return false
}
