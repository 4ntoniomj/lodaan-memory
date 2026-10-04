package mcptools

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// schemaFor infers the input schema of T. Property descriptions come from the
// jsonschema struct tags (in this SDK version the tag is the description itself,
// with no "description=" prefix, and it must not start with "WORD=").
func schemaFor[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("mcptools: esquema de entrada inválido: %v", err))
	}
	return s
}

// setEnum restricts the string property prop of the object schema s to vals.
func setEnum(s *jsonschema.Schema, prop string, vals ...string) {
	p := s.Properties[prop]
	if p == nil {
		panic(fmt.Sprintf("mcptools: el esquema no tiene la propiedad %q", prop))
	}
	p.Enum = make([]any, len(vals))
	for i, v := range vals {
		p.Enum[i] = v
	}
}
