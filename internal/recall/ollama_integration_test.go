package recall

// Integration tests against a real Ollama. They only run with LODAN_OLLAMA_IT=1
// and skip (with the reason) when Ollama or the model are not available.
//
//	LODAN_OLLAMA_IT=1 go test ./internal/recall/ -run 'TestCalibracionSimilitudes|TestParafrasisReal' -v -timeout 15m

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"lodan/internal/database"
	"lodan/internal/embedding"
	"lodan/internal/memory"
	"lodan/internal/session"
	"lodan/internal/topic"
)

const (
	itOllamaURL    = "http://127.0.0.1:11434"
	itDefaultModel = "embeddinggemma"
	itDims         = 768
	// Default thresholds of the plan (spec 001).
	itDupSimilarity   = 0.92
	itTopicSimilarity = 0.85
	// itEmbedTimeout is embed_timeout_ms of the memory service in TestParafrasisReal.
	itEmbedTimeout = 30 * time.Second
)

// ollamaForTest returns a client for the real Ollama, or skips the test if the
// integration tests are not enabled, the server does not answer or the model
// does not exist. The probe embedding also loads the model in memory.
func ollamaForTest(t *testing.T) *embedding.Ollama {
	t.Helper()
	if os.Getenv("LODAN_OLLAMA_IT") != "1" {
		t.Skip("define LODAN_OLLAMA_IT=1 para ejecutar los tests de integración contra Ollama real")
	}
	model := os.Getenv("LODAN_EMBED_MODEL")
	if model == "" {
		model = itDefaultModel
	}
	emb, err := embedding.NewOllama(embedding.Options{
		BaseURL:   itOllamaURL,
		Model:     model,
		Dims:      itDims,
		KeepAlive: "10m",
		Timeout:   2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("no se pudo crear el cliente de Ollama: %v", err)
	}

	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPing()
	if err := emb.Ping(pingCtx); err != nil {
		t.Skipf("se omite el test: Ollama no responde en %s: %v", itOllamaURL, err)
	}

	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelProbe()
	if _, err := emb.EmbedQuery(probeCtx, "prueba"); err != nil {
		if strings.Contains(err.Error(), "código 404") {
			t.Skipf("se omite el test: el modelo %q no existe en Ollama (ollama pull %s): %v", model, model, err)
		}
		if errors.Is(err, embedding.ErrUnavailable) {
			t.Skipf("se omite el test: Ollama no está disponible: %v", err)
		}
		t.Fatalf("falló el embedding de prueba con el modelo %q: %v", model, err)
	}
	t.Logf("modelo %q, %d dimensiones, %s", model, itDims, itOllamaURL)
	return emb
}

// docBatch collects distinct documents so they are embedded in a single request.
type docBatch struct {
	docs  []embedding.Document
	index map[embedding.Document]int
	vecs  [][]float32
}

func newDocBatch() *docBatch {
	return &docBatch{index: make(map[embedding.Document]int)}
}

func (b *docBatch) add(d embedding.Document) {
	if _, ok := b.index[d]; ok {
		return
	}
	b.index[d] = len(b.docs)
	b.docs = append(b.docs, d)
}

func (b *docBatch) embed(ctx context.Context, emb embedding.Embedder) error {
	vecs, err := emb.EmbedDocuments(ctx, b.docs)
	if err != nil {
		return err
	}
	b.vecs = vecs
	return nil
}

func (b *docBatch) vec(d embedding.Document) []float32 { return b.vecs[b.index[d]] }

// itTopicDoc is the document that topic embeds for a slug: spaces, empty title.
func itTopicDoc(slug string) embedding.Document {
	return embedding.Document{Text: strings.ReplaceAll(slug, "-", " ")}
}

// simStats accumulates the min, max and mean of a group of similarities.
type simStats struct {
	min, max, sum float64
	n             int
}

func (s *simStats) add(v float64) {
	if s.n == 0 || v < s.min {
		s.min = v
	}
	if s.n == 0 || v > s.max {
		s.max = v
	}
	s.sum += v
	s.n++
}

func (s *simStats) mean() float64 {
	if s.n == 0 {
		return 0
	}
	return s.sum / float64(s.n)
}

// itRecord is a record as memory embeds it: title and content.
type itRecord struct{ title, text string }

func (r itRecord) doc() embedding.Document { return embedding.Document{Title: r.title, Text: r.text} }

func TestCalibracionSimilitudes(t *testing.T) {
	emb := ollamaForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	topicPairs := []struct{ group, a, b string }{
		{"equivalente", "gym", "entrenamiento"},
		{"equivalente", "gimnasio", "entrenamiento"},
		{"equivalente", "coche", "vehiculo"},
		{"equivalente", "trabajo", "empleo"},
		{"equivalente", "salud", "medico"},
		{"distinto", "coche", "entrenamiento"},
		{"distinto", "lodan", "entrenamiento"},
		{"distinto", "trabajo", "salud"},
		{"distinto", "notas-diarias", "coche"},
		{"distinto", "recetas", "finanzas"},
	}

	// Record pairs. Each group reuses the same base record where it makes sense.
	piernas := itRecord{"Entreno de piernas", "Hoy he entrenado piernas: sentadillas 4x8 con 80 kg y prensa 3x12."}
	aceite := itRecord{"Cambio de aceite", "Cambié el aceite del coche en el taller de Paco por 85 euros."}
	reunion := itRecord{"Reunión de equipo", "La reunión semanal del equipo es los lunes a las 10:00 en la sala azul."}
	recordPairs := []struct {
		group string
		a, b  itRecord
	}{
		// Almost duplicates: same sentence with a minor change.
		{"casi-duplicado", piernas, itRecord{"Entreno de piernas", "Hoy he entrenado piernas: sentadillas 4x8 con 82 kg y prensa 3x12."}},
		{"casi-duplicado", aceite, itRecord{"Cambio de aceite", "Cambié el aceite del coche en el taller de Paco por 90 euros."}},
		{"casi-duplicado", reunion, itRecord{"Reunión de equipo", "La reunión semanal del equipo es los lunes a las 10:30 en la sala azul."}},
		{"casi-duplicado",
			itRecord{"Base de datos de lodan", "Lodan usará PostgreSQL con pgvector para guardar los embeddings."},
			itRecord{"Base de datos de lodan", "Lodan usará PostgreSQL con pgvector para almacenar los embeddings."}},

		// Paraphrases: same fact, different words.
		{"parafrasis",
			itRecord{"Horario de entreno", "Entreno por las tardes, a las siete, tres días a la semana."},
			itRecord{"Horario de gimnasio", "Suelo ir al gimnasio a las 19:00 unos tres días por semana."}},
		{"parafrasis",
			itRecord{"Seguro del coche", "El seguro del coche vence el 15 de marzo y lo renuevo con Mapfre."},
			itRecord{"Renovación del seguro", "La póliza del vehículo caduca a mediados de marzo; la prorrogo en Mapfre."}},
		{"parafrasis",
			itRecord{"Trabajo remoto", "Trabajo desde casa dos días a la semana, martes y jueves."},
			itRecord{"Teletrabajo", "Hago teletrabajo los martes y los jueves, el resto voy a la oficina."}},
		{"parafrasis",
			itRecord{"Editor de código", "Prefiero Neovim como editor para programar en Go."},
			itRecord{"Herramienta favorita", "Para escribir código en Go me quedo con Neovim."}},

		// Related but different facts.
		{"relacionado", piernas, itRecord{"Entreno de espalda", "Dominadas 4x6 y remo con barra 3x10 con 60 kg."}},
		{"relacionado", aceite, itRecord{"Cambio de neumáticos", "Cambié los neumáticos del coche en el taller de Paco por 320 euros."}},
		{"relacionado", reunion, itRecord{"Reunión con cliente", "La reunión con el cliente de Valencia es el jueves a las 16:00."}},

		// Unrelated.
		{"no-relacionado", piernas, itRecord{"Renovación del pasaporte", "El pasaporte caduca en 2029; hay que pedir cita en la comisaría."}},
		{"no-relacionado",
			itRecord{"Seguro del coche", "El seguro del coche vence el 15 de marzo y lo renuevo con Mapfre."},
			itRecord{"Receta de lentejas", "Las lentejas llevan chorizo, zanahoria y un poco de pimentón."}},
		{"no-relacionado",
			itRecord{"Trabajo remoto", "Trabajo desde casa dos días a la semana, martes y jueves."},
			itRecord{"Cumpleaños de mi madre", "El cumpleaños de mi madre es el 3 de noviembre; le gustan las flores."}},
		{"no-relacionado",
			itRecord{"Editor de código", "Prefiero Neovim como editor para programar en Go."},
			itRecord{"Vacaciones en Asturias", "En agosto iremos a Asturias una semana, con alojamiento cerca de Llanes."}},
	}

	queryTopicPairs := []struct {
		query   string
		topic   string
		correct bool
	}{
		{"¿qué hice en el último entreno?", "entrenamiento", true},
		{"¿qué hice en el último entreno?", "coche", false},
		{"¿qué hice en el último entreno?", "trabajo", false},
		{"¿cuándo toca la ITV?", "coche", true},
		{"¿cuándo toca la ITV?", "entrenamiento", false},
		{"¿cuándo toca la ITV?", "trabajo", false},
	}

	// One single batch with every distinct document.
	batch := newDocBatch()
	for _, p := range topicPairs {
		batch.add(itTopicDoc(p.a))
		batch.add(itTopicDoc(p.b))
	}
	for _, p := range recordPairs {
		batch.add(p.a.doc())
		batch.add(p.b.doc())
	}
	for _, p := range queryTopicPairs {
		batch.add(itTopicDoc(p.topic))
	}
	start := time.Now()
	if err := batch.embed(ctx, emb); err != nil {
		t.Fatalf("EmbedDocuments falló: %v", err)
	}
	queries := make(map[string][]float32)
	for _, p := range queryTopicPairs {
		if _, ok := queries[p.query]; ok {
			continue
		}
		v, err := emb.EmbedQuery(ctx, p.query)
		if err != nil {
			t.Fatalf("EmbedQuery(%q) falló: %v", p.query, err)
		}
		queries[p.query] = v
	}
	t.Logf("%d documentos y %d consultas embebidos en %s", len(batch.docs), len(queries), time.Since(start).Round(time.Millisecond))

	// Topics.
	topicStats := map[string]*simStats{"equivalente": {}, "distinto": {}}
	t.Logf("--- Temas (slug con espacios, documento sin título) ---")
	for _, p := range topicPairs {
		sim := embedding.Cosine(batch.vec(itTopicDoc(p.a)), batch.vec(itTopicDoc(p.b)))
		topicStats[p.group].add(sim)
		t.Logf("%-12s %.4f  %s <-> %s", p.group, sim, p.a, p.b)
	}

	// Records.
	recStats := map[string]*simStats{"casi-duplicado": {}, "parafrasis": {}, "relacionado": {}, "no-relacionado": {}}
	t.Logf("--- Registros (título + contenido, documento) ---")
	for _, p := range recordPairs {
		sim := embedding.Cosine(batch.vec(p.a.doc()), batch.vec(p.b.doc()))
		recStats[p.group].add(sim)
		t.Logf("%-14s %.4f  [%s] <-> [%s]", p.group, sim, p.a.title, p.b.title)
	}

	// Queries against topics, as recall.Detect does.
	var queryOK, queryKO simStats
	t.Logf("--- Consulta (prefijo de consulta) frente a tema (documento) ---")
	for _, p := range queryTopicPairs {
		sim := embedding.Cosine(queries[p.query], batch.vec(itTopicDoc(p.topic)))
		label := "incorrecto"
		if p.correct {
			label = "correcto"
			queryOK.add(sim)
		} else {
			queryKO.add(sim)
		}
		t.Logf("%-10s %.4f  %q <-> %s", label, sim, p.query, p.topic)
	}

	// Summary, to decide the thresholds (plan: dup_similarity 0,92 and topic_similarity 0,85).
	eq, di := topicStats["equivalente"], topicStats["distinto"]
	nd, pa, re, un := recStats["casi-duplicado"], recStats["parafrasis"], recStats["relacionado"], recStats["no-relacionado"]
	t.Logf("=== RESUMEN (umbrales actuales: tema %.2f, duplicado %.2f) ===", itTopicSimilarity, itDupSimilarity)
	t.Logf("temas:     mínimo de equivalentes %.4f (media %.4f) | máximo de distintos %.4f (media %.4f)",
		eq.min, eq.mean(), di.max, di.mean())
	t.Logf("registros: mínimo de casi duplicados %.4f (media %.4f) | máximo de paráfrasis %.4f (media %.4f), relacionados %.4f (media %.4f), no relacionados %.4f (media %.4f)",
		nd.min, nd.mean(), pa.max, pa.mean(), re.max, re.mean(), un.max, un.mean())
	t.Logf("consulta:  mínimo con su tema correcto %.4f (media %.4f) | máximo con los incorrectos %.4f (media %.4f)",
		queryOK.min, queryOK.mean(), queryKO.max, queryKO.mean())

	// Only the relative order is asserted; the thresholds are decided from the numbers.
	if eq.mean() <= di.mean() {
		t.Errorf("la media de temas equivalentes (%.4f) debería superar la de distintos (%.4f)", eq.mean(), di.mean())
	}
	if nd.mean() <= un.mean() {
		t.Errorf("la media de casi duplicados (%.4f) debería superar la de no relacionados (%.4f)", nd.mean(), un.mean())
	}
}

// itSharedWords returns the words of more than 3 letters that a and b share, so
// that a paraphrase really has nothing in common with its target.
func itSharedWords(a, b string) []string {
	split := func(s string) map[string]bool {
		out := make(map[string]bool)
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			if utf8.RuneCountInString(w) > 3 {
				out[w] = true
			}
		}
		return out
	}
	wa, wb := split(a), split(b)
	var shared []string
	for w := range wa {
		if wb[w] {
			shared = append(shared, w)
		}
	}
	return shared
}

// Criterion 9: a paraphrase without words in common finds its record.
func TestParafrasisReal(t *testing.T) {
	emb := ollamaForTest(t)
	tc := database.NewTestCluster(t, itDims)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	mgr := session.NewManager(tc.Pool, 30*time.Minute)
	tr := mgr.NewTracker("test-ollama")
	resolver := topic.NewResolver(tc.Pool, emb, itTopicSimilarity)
	mem := memory.NewService(tc.Pool, emb, resolver, mgr, itDupSimilarity, itEmbedTimeout)
	svc := NewService(tc.Pool, emb, resolver, Options{TopicThreshold: itTopicSimilarity})

	items := []memory.Item{
		// 0..3 are the targets of the paraphrases.
		{Title: "Cambio de aceite", Content: "Cambié el aceite del coche en el taller de Paco",
			Kind: memory.KindEvent, Topics: []string{"coche"}},
		{Title: "Rutina de entreno", Content: "Entreno por las tardes, a las siete, tres días por semana",
			Kind: memory.KindPreference, Topics: []string{"entrenamiento"}},
		{Title: "Teletrabajo", Content: "Trabajo desde casa los martes y los jueves",
			Kind: memory.KindPreference, Topics: []string{"trabajo"}},
		{Title: "Base de datos de lodan", Content: "Decisión: usar PostgreSQL con pgvector para guardar embeddings",
			Kind: memory.KindDecision, Topics: []string{"lodan"}},
		// Distractors.
		{Title: "Sesión de espalda", Content: "Dominadas y remo con barra, buena sesión",
			Kind: memory.KindEvent, Topics: []string{"entrenamiento"}},
		{Title: "Carrera del domingo", Content: "Corrí diez kilómetros en cincuenta y cinco minutos",
			Kind: memory.KindEvent, Topics: []string{"entrenamiento"}},
		{Title: "ITV", Content: "La ITV del coche caduca en noviembre",
			Kind: memory.KindFact, Topics: []string{"coche"}},
		{Title: "Neumáticos", Content: "Los neumáticos delanteros se cambiaron en septiembre",
			Kind: memory.KindEvent, Topics: []string{"coche"}},
		{Title: "Reunión semanal", Content: "La reunión de equipo es los lunes a las diez",
			Kind: memory.KindFact, Topics: []string{"trabajo"}},
		{Title: "Cliente de Valencia", Content: "Presentar la propuesta al cliente de Valencia el jueves",
			Kind: memory.KindEvent, Topics: []string{"trabajo"}},
		{Title: "Receta de lentejas", Content: "Las lentejas llevan chorizo, zanahoria y pimentón",
			Kind: memory.KindNote, Topics: []string{"cocina"}},
		{Title: "Editor de código", Content: "Prefiero Neovim para programar en Go",
			Kind: memory.KindPreference, Topics: []string{"programacion"}},
	}
	paraphrases := []struct {
		query  string
		target int // index in items
	}{
		{"¿cuándo fue el último mantenimiento del vehículo?", 0},
		{"¿a qué hora suelo ir al gimnasio?", 1},
		{"¿qué días no tengo que ir a la oficina?", 2},
		{"¿qué motor relacional elegimos al persistir vectores?", 3},
	}

	// The queries must really share no words with their target (words of more than 3 letters).
	for _, p := range paraphrases {
		it := items[p.target]
		if shared := itSharedWords(p.query, it.Title+" "+it.Content); len(shared) > 0 {
			t.Fatalf("la consulta %q comparte palabras con su registro %q: %v", p.query, it.Title, shared)
		}
	}

	saved, err := mem.Remember(ctx, tr, items)
	if err != nil {
		t.Fatalf("Remember falló: %v", err)
	}
	if len(saved) != len(items) {
		t.Fatalf("Remember devolvió %d registros y se esperaban %d", len(saved), len(items))
	}
	pending := 0
	for i, s := range saved {
		if s.Duplicate {
			t.Fatalf("el registro %q se marcó como duplicado", items[i].Title)
		}
		if s.Pending {
			pending++
		}
	}
	if pending > 0 {
		// The 30 s deadline expired on a slow CPU: complete the embeddings by hand.
		t.Logf("%d registros quedaron pendientes por el plazo de %s; se completan con FillPending", pending, itEmbedTimeout)
		if _, err := mem.FillPending(ctx, 100); err != nil {
			t.Fatalf("FillPending de registros falló: %v", err)
		}
	}
	// Topics created after the deadline would have no embedding yet; this is a no-op otherwise.
	if _, err := resolver.FillPending(ctx, 100); err != nil {
		t.Fatalf("FillPending de temas falló: %v", err)
	}

	for _, p := range paraphrases {
		it, id := items[p.target], saved[p.target].ID
		t.Run(it.Title, func(t *testing.T) {
			res, err := svc.Recall(ctx, Request{Query: p.query, Limit: 10})
			if err != nil {
				t.Fatalf("Recall(%q) falló: %v", p.query, err)
			}
			detected := "ninguno"
			if res.Topic != nil {
				detected = res.Topic.Slug
				if res.TopicDetected {
					detected += " (detectado)"
				}
			}

			sections := []struct {
				name  string
				items []Item
			}{{"Profile", res.Profile}, {"Events", res.Events}, {"Results", res.Results}}
			where := ""
			for _, s := range sections {
				if i := find(s.items, id); i >= 0 {
					where = fmt.Sprintf("%s[%d]", s.name, i)
					if i >= 10 {
						t.Errorf("#%d está en %s, fuera de las 10 primeras posiciones", id, where)
					}
					break
				}
			}
			t.Logf("consulta %q -> #%d %q: posición %s (tema: %s; Profile %v, Events %v, Results %v)",
				p.query, id, it.Title, orNone(where), detected, idsOf(res.Profile), idsOf(res.Events), idsOf(res.Results))
			if where == "" {
				t.Errorf("#%d %q no aparece en Profile, Events ni Results para %q", id, it.Title, p.query)
			}
		})
	}
}

// orNone returns s, or "no aparece" if it is empty.
func orNone(s string) string {
	if s == "" {
		return "no aparece"
	}
	return s
}
