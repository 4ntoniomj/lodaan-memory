package mcptools

import (
	"encoding/json"
	"reflect"
	"testing"
)

// These tests replay the shapes that real sessions sent between 2026-11-06 and
// 2026-11-14 and that the strict schema rejected before the handler ran. Each
// case is the arguments exactly as the model wrote them plus the values the
// handler must receive.

func decode(t *testing.T, tool string, target reflect.Type, raw string) any {
	t.Helper()
	out := CoerceArguments(tool, target, json.RawMessage(raw))
	dest := reflect.New(target)
	if err := json.Unmarshal(out, dest.Interface()); err != nil {
		t.Fatalf("unmarshal tras normalizar: %v (salida %s)", err, out)
	}
	return dest.Elem().Interface()
}

func TestCoerceRememberNestedItemWrapper(t *testing.T) {
	// Shape of 2026-11-07: items wrapped as {"item": {...}} (36 calls).
	raw := `{"items":{"item":{"title":"QA de subagentes","content":"2026-11-07. QA real de subagentes.",
		"kind":"fact","tags":["proyecto-4mj-pi","herramienta-pi"]}}}`
	in := decode(t, "remember", reflect.TypeFor[rememberInput](), raw).(rememberInput)
	if len(in.Items) != 1 {
		t.Fatalf("items = %d, se esperaba 1", len(in.Items))
	}
	got := in.Items[0]
	if got.Title != "QA de subagentes" {
		t.Errorf("title = %q", got.Title)
	}
	if got.Kind != "fact" {
		t.Errorf("kind = %q, se esperaba fact", got.Kind)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "proyecto-4mj-pi" || got.Tags[1] != "herramienta-pi" {
		t.Errorf("tags = %v", got.Tags)
	}
}

func TestCoerceRememberSpanishKindAndExtraFields(t *testing.T) {
	// Shape of 2026-11-13: kind "decisión", extra fields que/por_que.
	raw := `{"preview":true,"items":[{"title":"prueba preview","content":"prueba","kind":"decisión",
		"tags":["proyecto-lodan-memory"],"que":"x","por_que":"y"}]}`
	in := decode(t, "remember", reflect.TypeFor[rememberInput](), raw).(rememberInput)
	if !in.Preview {
		t.Error("preview no llegó")
	}
	if len(in.Items) != 1 || in.Items[0].Kind != "decision" {
		t.Fatalf("kind = %q, se esperaba decision", in.Items[0].Kind)
	}
}

func TestCoerceRememberFlatItemAtRoot(t *testing.T) {
	// Shape of 2026-11-13: one record written flat, without items (2 calls).
	raw := `{"kind":"decisión","text":"2026-11-13, agm-cosechas-go: el aprendizaje es del CLIENTE.",
		"tags":["proyecto-agm-cosechas"],"title":"Aprendizaje por cliente"}`
	in := decode(t, "remember", reflect.TypeFor[rememberInput](), raw).(rememberInput)
	if len(in.Items) != 1 {
		t.Fatalf("items = %d, se esperaba 1", len(in.Items))
	}
	if in.Items[0].Content == "" {
		t.Error("text no se convirtió en content")
	}
	if in.Items[0].Kind != "decision" {
		t.Errorf("kind = %q", in.Items[0].Kind)
	}
}

func TestCoerceRememberTagsAsStringAndMap(t *testing.T) {
	// Shapes of 2026-11-09 and 2026-11-12: tags as one string (21 calls) or as
	// a numeric map.
	for _, raw := range []string{
		`{"items":[{"title":"t","content":"c","kind":"note","tags":"proyecto-agm, herramienta-go"}]}`,
		`{"items":[{"title":"t","content":"c","kind":"note","tags":{"produto":"x"}}]}`,
	} {
		in := decode(t, "remember", reflect.TypeFor[rememberInput](), raw).(rememberInput)
		if len(in.Items) != 1 {
			t.Fatalf("items = %d en %s", len(in.Items), raw)
		}
		if len(in.Items[0].Tags) == 0 {
			t.Errorf("sin etiquetas en %s", raw)
		}
	}
}

func TestCoerceRecallQueriesAsString(t *testing.T) {
	// Shape of 2026-11-13: queries as a plain string (1 call).
	in := decode(t, "recall", reflect.TypeFor[recallInput](), `{"queries":"lodan conexiones"}`).(recallInput)
	if len(in.Queries) != 1 || in.Queries[0] != "lodan conexiones" {
		t.Fatalf("queries = %v", in.Queries)
	}
}

func TestCoerceRecallQueriesWrapped(t *testing.T) {
	// Shape of 2026-11-07: queries as {"item": [...]}.
	in := decode(t, "recall", reflect.TypeFor[recallInput](), `{"queries":{"item":["subagentes QA","rol orquestador"]}}`).(recallInput)
	if len(in.Queries) != 2 || in.Queries[1] != "rol orquestador" {
		t.Fatalf("queries = %v", in.Queries)
	}
}

func TestCoerceRecallLimitAsString(t *testing.T) {
	in := decode(t, "recall", reflect.TypeFor[recallInput](), `{"query":"x","limit":"12"}`).(recallInput)
	if in.Limit != 12 {
		t.Fatalf("limit = %d, se esperaba 12", in.Limit)
	}
}

func TestCoerceReviseTargetAlias(t *testing.T) {
	// Shape of 2026-11-13: relate with target/relation instead of with_id.
	raw := `{"ops":[{"action":"relate","id":"3024","target":"2856","relation":"supersedes"}]}`
	in := decode(t, "revise", reflect.TypeFor[reviseInput](), raw).(reviseInput)
	if len(in.Ops) != 1 {
		t.Fatalf("ops = %d", len(in.Ops))
	}
	op := in.Ops[0]
	if op.ID != 3024 || op.WithID != 2856 {
		t.Fatalf("id=%d with_id=%d, se esperaba 3024/2856", op.ID, op.WithID)
	}
	if op.Action != "relate" || op.Relation != "supersedes" {
		t.Fatalf("action=%q relation=%q", op.Action, op.Relation)
	}
}

func TestCoerceReviseHashedIdAndSpanishAction(t *testing.T) {
	raw := `{"id":"#123","action":"actualizar","title":"nuevo"}`
	in := decode(t, "revise", reflect.TypeFor[reviseInput](), raw).(reviseInput)
	if in.ID != 123 {
		t.Fatalf("id = %d, se esperaba 123", in.ID)
	}
	if in.Action != "update" {
		t.Fatalf("action = %q, se esperaba update", in.Action)
	}
}

func TestCoerceDropsUnknownProperties(t *testing.T) {
	// Shape of 2026-11-11: remember with a session field at the root.
	raw := `{"items":[{"title":"t","content":"c","kind":"note","tags":["skill-odd"]}],"session":"x","id":"7"}`
	in := decode(t, "remember", reflect.TypeFor[rememberInput](), raw).(rememberInput)
	if len(in.Items) != 1 || in.Items[0].Title != "t" {
		t.Fatalf("items = %+v", in.Items)
	}
}

func TestCoerceDoubleEncodedArguments(t *testing.T) {
	// Shape of 2026-11-14: the whole argument object sent as a JSON string.
	raw := `"{\"queries\":\"carteles\"}"`
	in := decode(t, "recall", reflect.TypeFor[recallInput](), raw).(recallInput)
	if len(in.Queries) != 1 || in.Queries[0] != "carteles" {
		t.Fatalf("queries = %v", in.Queries)
	}
}

func TestCoerceSessionSpanishAction(t *testing.T) {
	in := decode(t, "session", reflect.TypeFor[sessionInput](), `{"action":"cerrar","summary":"fin"}`).(sessionInput)
	if in.Action != "end" {
		t.Fatalf("action = %q, se esperaba end", in.Action)
	}
}

func TestCoerceUnknownKindSurvivesForTheHandler(t *testing.T) {
	// An unknown kind is not rewritten: the handler rejects it with the list of
	// valid ones, so the model fixes the call instead of losing the record.
	in := decode(t, "remember", reflect.TypeFor[rememberInput](),
		`{"items":[{"title":"t","content":"c","kind":"apunte raro","tags":["skill-odd"]}]}`).(rememberInput)
	if in.Items[0].Kind != "apunte raro" {
		t.Fatalf("kind = %q, se esperaba el original", in.Items[0].Kind)
	}
}

func TestCoerceValidArgumentsUnchanged(t *testing.T) {
	raw := `{"items":[{"title":"t","content":"c","kind":"event","tags":["proyecto-todo-de-una"],"key":"k/x"}]}`
	in := decode(t, "remember", reflect.TypeFor[rememberInput](), raw).(rememberInput)
	it := in.Items[0]
	if it.Kind != "event" || it.Key != "k/x" || len(it.Tags) != 1 {
		t.Fatalf("ítem válido alterado: %+v", it)
	}
}
