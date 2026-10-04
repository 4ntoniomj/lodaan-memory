package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturedRequest mirrors the JSON body sent to /api/embed.
type capturedRequest struct {
	Model      string          `json:"model"`
	Input      []string        `json:"input"`
	Truncate   bool            `json:"truncate"`
	KeepAlive  json.RawMessage `json:"keep_alive"`
	Dimensions int             `json:"dimensions"`
}

type recorder struct {
	mu   sync.Mutex
	reqs []capturedRequest
}

func (r *recorder) add(req capturedRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
}

func (r *recorder) last(t *testing.T) capturedRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		t.Fatal("el servidor no recibió ninguna petición")
	}
	return r.reqs[len(r.reqs)-1]
}

// unitVectors returns n vectors of the given dimension.
func unitVectors(n, dims int) [][]float32 {
	out := make([][]float32, n)
	for i := range out {
		out[i] = make([]float32, dims)
		out[i][i%dims] = 1
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// newEmbedServer starts a fake Ollama that records each /api/embed request and
// answers with one unit vector of the given dimension per input.
func newEmbedServer(t *testing.T, dims int) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/embed" {
			http.Error(w, "ruta inesperada", http.StatusNotFound)
			return
		}
		var req capturedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rec.add(req)
		writeJSON(w, map[string]any{
			"model":      req.Model,
			"embeddings": unitVectors(len(req.Input), dims),
		})
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func mustOllama(t *testing.T, opts Options) *Ollama {
	t.Helper()
	o, err := NewOllama(opts)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	return o
}

func TestNewOllamaValida(t *testing.T) {
	if _, err := NewOllama(Options{Dims: 8}); err == nil {
		t.Error("se esperaba error sin modelo")
	}
	if _, err := NewOllama(Options{Model: "m"}); err == nil {
		t.Error("se esperaba error sin dimensiones")
	}
	o := mustOllama(t, Options{Model: "m", Dims: 8})
	if o.Model() != "m" || o.Dims() != 8 {
		t.Errorf("Model/Dims inesperados: %q %d", o.Model(), o.Dims())
	}
}

func TestPrefijosPorModelo(t *testing.T) {
	const dims = 8
	cases := []struct {
		model        string
		query        string
		docWithTitle string
		docNoTitle   string
	}{
		{
			model:        "embeddinggemma:300m-qat-q4_0",
			query:        "task: search result | query: hola",
			docWithTitle: "title: Mi título | text: cuerpo",
			docNoTitle:   "title: none | text: cuerpo",
		},
		{
			model:        "embeddinggemma",
			query:        "task: search result | query: hola",
			docWithTitle: "title: Mi título | text: cuerpo",
			docNoTitle:   "title: none | text: cuerpo",
		},
		{
			model:        "qwen3-embedding:0.6b",
			query:        "Instruct: Given a search query, retrieve relevant memories\nQuery:hola",
			docWithTitle: "Mi título\n\ncuerpo",
			docNoTitle:   "cuerpo",
		},
		{
			model:        "nomic-embed-text-v2-moe",
			query:        "search_query: hola",
			docWithTitle: "search_document: cuerpo",
			docNoTitle:   "search_document: cuerpo",
		},
		{
			model:        "bge-m3",
			query:        "hola",
			docWithTitle: "Mi título\n\ncuerpo",
			docNoTitle:   "cuerpo",
		},
		{
			model:        "modelo-desconocido:latest",
			query:        "hola",
			docWithTitle: "Mi título\n\ncuerpo",
			docNoTitle:   "cuerpo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			srv, rec := newEmbedServer(t, dims)
			o := mustOllama(t, Options{BaseURL: srv.URL, Model: tc.model, Dims: dims})
			ctx := context.Background()

			if _, err := o.EmbedQuery(ctx, "hola"); err != nil {
				t.Fatalf("EmbedQuery: %v", err)
			}
			req := rec.last(t)
			if req.Model != tc.model {
				t.Errorf("modelo enviado = %q, se esperaba %q", req.Model, tc.model)
			}
			if want := []string{tc.query}; !slices.Equal(req.Input, want) {
				t.Errorf("consulta enviada = %q, se esperaba %q", req.Input, want)
			}

			docs := []Document{{Title: "Mi título", Text: "cuerpo"}, {Text: "cuerpo"}}
			vecs, err := o.EmbedDocuments(ctx, docs)
			if err != nil {
				t.Fatalf("EmbedDocuments: %v", err)
			}
			if len(vecs) != 2 {
				t.Fatalf("se esperaban 2 vectores, hay %d", len(vecs))
			}
			req = rec.last(t)
			if want := []string{tc.docWithTitle, tc.docNoTitle}; !slices.Equal(req.Input, want) {
				t.Errorf("documentos enviados = %q, se esperaba %q", req.Input, want)
			}
			if !req.Truncate {
				t.Error("truncate debería enviarse a true")
			}
			if req.Dimensions != dims {
				t.Errorf("dimensions = %d, se esperaba %d", req.Dimensions, dims)
			}
		})
	}
}

func TestKeepAlive(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // raw JSON; "" means the field is absent
	}{
		{"negativo viaja como número", "-1", "-1"},
		{"duración viaja como string", "5m", `"5m"`},
		{"vacío se omite", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, rec := newEmbedServer(t, 4)
			o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 4, KeepAlive: tc.in})
			if _, err := o.EmbedQuery(context.Background(), "x"); err != nil {
				t.Fatalf("EmbedQuery: %v", err)
			}
			if got := string(rec.last(t).KeepAlive); got != tc.want {
				t.Errorf("keep_alive = %q, se esperaba %q", got, tc.want)
			}
		})
	}
}

func TestLoteVacioNoLlamaALaRed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no debería haber petición de red: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 4})
	for _, docs := range [][]Document{nil, {}} {
		vecs, err := o.EmbedDocuments(context.Background(), docs)
		if err != nil || vecs != nil {
			t.Errorf("lote vacío: vecs=%v err=%v, se esperaba nil, nil", vecs, err)
		}
	}
}

func TestNumeroDeVectoresIncorrecto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"embeddings": unitVectors(1, 4)})
	}))
	defer srv.Close()

	o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 4})
	_, err := o.EmbedDocuments(context.Background(), []Document{{Text: "a"}, {Text: "b"}})
	if err == nil {
		t.Fatal("se esperaba error por número de vectores")
	}
	if !strings.Contains(err.Error(), "vectores") {
		t.Errorf("el error debería mencionar los vectores: %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("un vector de menos no es ErrUnavailable")
	}
}

func TestDimensionesIncorrectas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"embeddings": unitVectors(1, 3)})
	}))
	defer srv.Close()

	o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 4})
	_, err := o.EmbedQuery(context.Background(), "a")
	if err == nil {
		t.Fatal("se esperaba error por dimensiones")
	}
	if !strings.Contains(err.Error(), "dimensiones") {
		t.Errorf("el error debería mencionar las dimensiones: %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("una dimensión errónea no es ErrUnavailable")
	}
}

func TestServidorCaido(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // ahora nadie escucha en esa dirección

	o := mustOllama(t, Options{BaseURL: url, Model: "bge-m3", Dims: 4, Timeout: 2 * time.Second})
	ctx := context.Background()

	if _, err := o.EmbedQuery(ctx, "a"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("EmbedQuery: se esperaba ErrUnavailable, hay %v", err)
	}
	if _, err := o.EmbedDocuments(ctx, []Document{{Text: "a"}}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("EmbedDocuments: se esperaba ErrUnavailable, hay %v", err)
	}
	if err := o.Ping(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Ping: se esperaba ErrUnavailable, hay %v", err)
	}
}

func TestContextoCanceladoNoEsIndisponible(t *testing.T) {
	srv, _ := newEmbedServer(t, 4)
	o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 4})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := o.EmbedQuery(ctx, "a")
	if err == nil {
		t.Fatal("se esperaba error con el contexto cancelado")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("una cancelación no debe tratarse como ErrUnavailable")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("se esperaba context.Canceled: %v", err)
	}
}

func TestError500IncluyeElCuerpo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fallo interno del modelo", http.StatusInternalServerError)
	}))
	defer srv.Close()

	o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 4})
	_, err := o.EmbedQuery(context.Background(), "a")
	if err == nil {
		t.Fatal("se esperaba error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "500") || !strings.Contains(msg, "fallo interno del modelo") {
		t.Errorf("el error debería incluir el código y el cuerpo: %v", err)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Error("un 500 debería ser ErrUnavailable")
	}
}

func TestCodigosHTTPYErrUnavailable(t *testing.T) {
	cases := []struct {
		status          int
		wantUnavailable bool
	}{
		{http.StatusInternalServerError, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusNotFound, false},
		{http.StatusBadRequest, false},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "detalle del fallo", tc.status)
			}))
			defer srv.Close()

			o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 4})
			_, err := o.EmbedQuery(context.Background(), "a")
			if err == nil {
				t.Fatal("se esperaba error")
			}
			if got := errors.Is(err, ErrUnavailable); got != tc.wantUnavailable {
				t.Errorf("errors.Is(err, ErrUnavailable) = %v, se esperaba %v (%v)", got, tc.wantUnavailable, err)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) || !strings.Contains(err.Error(), "detalle del fallo") {
				t.Errorf("el error debería incluir el código y el cuerpo: %v", err)
			}
		})
	}
}

func TestTituloEnModelosSinFormatoPropio(t *testing.T) {
	for _, model := range []string{"bge-m3", "qwen3-embedding:0.6b", "modelo-desconocido"} {
		t.Run(model, func(t *testing.T) {
			srv, rec := newEmbedServer(t, 4)
			o := mustOllama(t, Options{BaseURL: srv.URL, Model: model, Dims: 4})
			docs := []Document{
				{Title: "Horario", Text: "martes y jueves"},
				{Title: "   ", Text: "solo texto"},
				{Text: "sin título"},
			}
			if _, err := o.EmbedDocuments(context.Background(), docs); err != nil {
				t.Fatalf("EmbedDocuments: %v", err)
			}
			want := []string{"Horario\n\nmartes y jueves", "solo texto", "sin título"}
			if got := rec.last(t).Input; !slices.Equal(got, want) {
				t.Errorf("documentos enviados = %q, se esperaba %q", got, want)
			}
		})
	}
}

func TestError500TruncaElCuerpo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()

	o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 4})
	_, err := o.EmbedQuery(context.Background(), "a")
	if err == nil {
		t.Fatal("se esperaba error")
	}
	if n := strings.Count(err.Error(), "x"); n != maxErrorBody {
		t.Errorf("el cuerpo incluido tiene %d bytes, se esperaban %d", n, maxErrorBody)
	}
}

func TestNormalizaLosVectores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"embeddings": [][]float32{{3, 4, 0}}})
	}))
	defer srv.Close()

	o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 3})
	v, err := o.EmbedQuery(context.Background(), "a")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 || v[2] != 0 {
		t.Errorf("vector normalizado inesperado: %v", v)
	}
}

func TestVectorNuloEsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"embeddings": [][]float32{{0, 0, 0}}})
	}))
	defer srv.Close()

	o := mustOllama(t, Options{BaseURL: srv.URL, Model: "bge-m3", Dims: 3})
	if _, err := o.EmbedQuery(context.Background(), "a"); err == nil {
		t.Error("se esperaba error con un vector nulo")
	}
}

func TestPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/version" {
			http.Error(w, "ruta inesperada", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"version": "0.0.0-test"})
	}))
	defer srv.Close()

	o := mustOllama(t, Options{BaseURL: srv.URL + "/", Model: "bge-m3", Dims: 4})
	if err := o.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

// --- FakeEmbedder y Cosine ---

func norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

func TestFakeDeterminista(t *testing.T) {
	ctx := context.Background()
	a := &FakeEmbedder{Dimensions: 128}
	b := &FakeEmbedder{Dimensions: 128}

	v1, err := a.EmbedQuery(ctx, "Entrenamiento de fuerza por la mañana")
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := a.EmbedQuery(ctx, "Entrenamiento de fuerza por la mañana")
	v3, _ := b.EmbedQuery(ctx, "entrenamiento, de fuerza. por la mañana!")
	if !slices.Equal(v1, v2) {
		t.Error("misma instancia, mismo texto: los vectores difieren")
	}
	if !slices.Equal(v1, v3) {
		t.Error("otra instancia, mismo texto en minúsculas y sin puntuación: los vectores difieren")
	}
	if len(v1) != 128 || a.Dims() != 128 {
		t.Errorf("dimensión inesperada: %d / %d", len(v1), a.Dims())
	}
	if a.Model() != "fake" {
		t.Errorf("Model() = %q", a.Model())
	}
	if (&FakeEmbedder{}).Dims() != defaultFakeDims {
		t.Errorf("la dimensión por defecto debería ser %d", defaultFakeDims)
	}
}

func TestFakeNormalizado(t *testing.T) {
	f := &FakeEmbedder{Dimensions: 64}
	docs := []Document{
		{Title: "Horario", Text: "entreno los martes y jueves"},
		{Text: ""},
		{Title: "a", Text: "b c d e f g h i j k l m n o p"},
	}
	vecs, err := f.EmbedDocuments(context.Background(), docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(docs) {
		t.Fatalf("se esperaban %d vectores, hay %d", len(docs), len(vecs))
	}
	for i, v := range vecs {
		if math.Abs(norm(v)-1) > 1e-5 {
			t.Errorf("vector %d con norma %v, se esperaba 1", i, norm(v))
		}
	}
	if vecs, err := f.EmbedDocuments(context.Background(), nil); vecs != nil || err != nil {
		t.Errorf("lote vacío: %v %v", vecs, err)
	}
}

func TestFakeTextoVacio(t *testing.T) {
	f := &FakeEmbedder{Dimensions: 16}
	for _, q := range []string{"", "   ", "¿?!"} {
		v, err := f.EmbedQuery(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if v[0] != 1 {
			t.Errorf("%q: v[0] = %v, se esperaba 1", q, v[0])
		}
		for i := 1; i < len(v); i++ {
			if v[i] != 0 {
				t.Errorf("%q: v[%d] = %v, se esperaba 0", q, i, v[i])
			}
		}
	}
}

func TestFakeSimilares(t *testing.T) {
	ctx := context.Background()
	f := &FakeEmbedder{Dimensions: 256}
	base, _ := f.EmbedQuery(ctx, "entrenamiento fuerza mañana gimnasio pesas")
	parecido, _ := f.EmbedQuery(ctx, "entrenamiento fuerza gimnasio pesas tarde")
	distinto, _ := f.EmbedQuery(ctx, "receta tarta manzana canela horno")

	sp := Cosine(base, parecido)
	sd := Cosine(base, distinto)
	if sp <= sd {
		t.Errorf("el texto parecido (%.3f) debería puntuar más que el distinto (%.3f)", sp, sd)
	}
	if sp < 0.5 {
		t.Errorf("similitud del texto parecido demasiado baja: %.3f", sp)
	}
	if sd > 0.5 {
		t.Errorf("similitud del texto distinto demasiado alta: %.3f", sd)
	}
}

func TestFakeAlias(t *testing.T) {
	ctx := context.Background()
	f := &FakeEmbedder{Dimensions: 256}
	coche, _ := f.EmbedQuery(ctx, "coche rojo")
	auto, _ := f.EmbedQuery(ctx, "automovil azul")
	if Cosine(coche, auto) > 0.5 {
		t.Fatalf("sin alias no deberían parecerse: %.3f", Cosine(coche, auto))
	}

	f.WithAlias("Coche", "automovil").WithAlias("rojo", "azul")
	coche, _ = f.EmbedQuery(ctx, "coche rojo")
	auto, _ = f.EmbedQuery(ctx, "automovil azul")
	if c := Cosine(coche, auto); c < 0.999 {
		t.Errorf("con alias deberían ser idénticos, similitud %.4f", c)
	}
}

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"idénticos", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"opuestos", []float32{1, 0}, []float32{-1, 0}, -1},
		{"ortogonales", []float32{1, 0}, []float32{0, 1}, 0},
		{"escala irrelevante", []float32{1, 1}, []float32{5, 5}, 1},
		{"longitudes distintas", []float32{1, 0}, []float32{1, 0, 0}, 0},
		{"vacíos", nil, nil, 0},
		{"vector nulo", []float32{0, 0}, []float32{1, 1}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Cosine(tc.a, tc.b); math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("Cosine = %v, se esperaba %v", got, tc.want)
			}
		})
	}
}
