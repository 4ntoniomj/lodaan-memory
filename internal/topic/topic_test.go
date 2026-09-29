package topic

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"lodan/internal/database"
	"lodan/internal/embedding"
)

const (
	testDims      = 64
	testThreshold = 0.9
	testTimeout   = 60 * time.Second
)

func TestNormalize(t *testing.T) {
	long := strings.Repeat("abcdefghi ", 10) // 99 caracteres con guiones
	cases := []struct {
		in, want string
	}{
		{"Entrenamiento", "entrenamiento"},
		{"Mi Coche!!", "mi-coche"},
		{"  Gimnasio/Pierna ", "gimnasio-pierna"},
		{"Ñandú Ç", "nandu-c"},
		{"ÀÉÎÖÜ", "aeiou"},
		{"àèìòù äëïöü âêîôû", "aeiou-aeiou-aeiou"},
		{"Año 2024", "ano-2024"},
		{"a---b__c", "a-b-c"},
		{"", ""},
		{"   ", ""},
		{"!!!", ""},
		{"--", ""},
		{"日本語", ""},
		{strings.Repeat("a", 100), strings.Repeat("a", 60)},
		{long, strings.TrimSuffix(strings.Repeat("abcdefghi-", 6), "-")},
		// Justo 60 caracteres seguidos de "-": se corta en el guion sin perder palabras.
		{strings.Repeat("a", 60) + " b", strings.Repeat("a", 60)},
	}
	for _, c := range cases {
		got := Normalize(c.in)
		if got != c.want {
			t.Errorf("Normalize(%q) = %q, se esperaba %q", c.in, got, c.want)
		}
		if len(got) > maxSlugLen {
			t.Errorf("Normalize(%q) tiene %d caracteres, el máximo es %d", c.in, len(got), maxSlugLen)
		}
	}
}

// unavailableEmbedder simulates Ollama being down.
type unavailableEmbedder struct{ dims int }

func (e unavailableEmbedder) EmbedDocuments(context.Context, []embedding.Document) ([][]float32, error) {
	return nil, fmt.Errorf("prueba: %w", embedding.ErrUnavailable)
}

func (e unavailableEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, fmt.Errorf("prueba: %w", embedding.ErrUnavailable)
}

func (e unavailableEmbedder) Model() string { return "unavailable" }
func (e unavailableEmbedder) Dims() int     { return e.dims }

func TestResolver(t *testing.T) {
	tc := database.NewTestCluster(t, testDims)
	fake := (&embedding.FakeEmbedder{Dimensions: testDims}).WithAlias("gym", "entrenamiento")

	// run ejecuta un subtest sobre tablas vacías y un contexto acotado.
	run := func(name string, f func(t *testing.T, ctx context.Context)) {
		t.Run(name, func(t *testing.T) {
			tc.Reset(t)
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			f(t, ctx)
		})
	}

	countTopics := func(t *testing.T, ctx context.Context) int {
		t.Helper()
		var n int
		if err := tc.Pool.QueryRow(ctx, `SELECT count(*) FROM topics`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	countPending := func(t *testing.T, ctx context.Context) int {
		t.Helper()
		var n int
		if err := tc.Pool.QueryRow(ctx, `SELECT count(*) FROM topics WHERE embedding IS NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	run("crea un tema nuevo", func(t *testing.T, ctx context.Context) {
		r := NewResolver(tc.Pool, fake, testThreshold)
		got, err := r.Resolve(ctx, []string{"Entrenamiento"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Topic.Slug != "entrenamiento" || got[0].Equivalent || got[0].Requested != "entrenamiento" {
			t.Fatalf("resolución inesperada: %+v", got)
		}
		if got[0].Topic.ID == 0 {
			t.Error("el tema no tiene ID")
		}
		if n := countTopics(t, ctx); n != 1 {
			t.Errorf("hay %d temas, se esperaba 1", n)
		}
		if n := countPending(t, ctx); n != 0 {
			t.Errorf("hay %d temas sin embedding, se esperaba 0", n)
		}
	})

	run("un slug existente se reutiliza", func(t *testing.T, ctx context.Context) {
		r := NewResolver(tc.Pool, fake, testThreshold)
		first, err := r.Resolve(ctx, []string{"mi coche"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := r.Resolve(ctx, []string{"Mi-Coche!"})
		if err != nil {
			t.Fatal(err)
		}
		if len(second) != 1 || second[0].Topic != first[0].Topic || second[0].Equivalent {
			t.Fatalf("segunda resolución = %+v, se esperaba el mismo tema que %+v", second, first)
		}
		if n := countTopics(t, ctx); n != 1 {
			t.Errorf("hay %d temas, se esperaba 1", n)
		}
	})

	run("equivalente por embedding (criterio 6)", func(t *testing.T, ctx context.Context) {
		r := NewResolver(tc.Pool, fake, testThreshold)
		base, err := r.Resolve(ctx, []string{"entrenamiento"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.Resolve(ctx, []string{"gym"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("resoluciones = %+v, se esperaba 1", got)
		}
		if got[0].Topic != base[0].Topic || !got[0].Equivalent || got[0].Requested != "gym" {
			t.Errorf("resolución = %+v, se esperaba entrenamiento con Equivalent y Requested=gym", got[0])
		}
		if n := countTopics(t, ctx); n != 1 {
			t.Errorf("hay %d temas, se esperaba 1 (no se debe crear 'gym')", n)
		}

		// Con una caché recién cargada de la base de datos (media precisión) sigue funcionando.
		r2 := NewResolver(tc.Pool, fake, testThreshold)
		if err := r2.Load(ctx); err != nil {
			t.Fatal(err)
		}
		got, err = r2.Resolve(ctx, []string{"gym"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Topic != base[0].Topic || !got[0].Equivalent {
			t.Errorf("tras Load, resolución = %+v, se esperaba entrenamiento equivalente", got)
		}
	})

	run("temas distintos no se fusionan", func(t *testing.T, ctx context.Context) {
		r := NewResolver(tc.Pool, fake, testThreshold)
		got, err := r.Resolve(ctx, []string{"mi coche", "receta de pasta"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Topic.ID == got[1].Topic.ID || got[0].Equivalent || got[1].Equivalent {
			t.Fatalf("resoluciones inesperadas: %+v", got)
		}
		if n := countTopics(t, ctx); n != 2 {
			t.Errorf("hay %d temas, se esperaba 2", n)
		}
	})

	run("deduplica dentro de la misma llamada", func(t *testing.T, ctx context.Context) {
		r := NewResolver(tc.Pool, fake, testThreshold)
		got, err := r.Resolve(ctx, []string{"Entrenamiento", "", "!!!", "entrenamiento", "gym", "ENTRENAMIENTO "})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Topic.Slug != "entrenamiento" || got[0].Equivalent {
			t.Fatalf("resoluciones = %+v, se esperaba solo 'entrenamiento'", got)
		}
		if n := countTopics(t, ctx); n != 1 {
			t.Errorf("hay %d temas, se esperaba 1", n)
		}
	})

	run("Detect", func(t *testing.T, ctx context.Context) {
		r := NewResolver(tc.Pool, fake, testThreshold)
		if _, _, ok := r.Detect(ctx, []float32{1, 0}); ok {
			t.Error("Detect con la caché vacía devolvió ok")
		}
		res, err := r.Resolve(ctx, []string{"entrenamiento", "mi coche"})
		if err != nil {
			t.Fatal(err)
		}

		q, err := fake.EmbedQuery(ctx, "gym")
		if err != nil {
			t.Fatal(err)
		}
		got, sim, ok := r.Detect(ctx, q)
		if !ok || got != res[0].Topic || sim < testThreshold {
			t.Errorf("Detect(gym) = %+v, %v, %v; se esperaba entrenamiento con similitud >= %v", got, sim, ok, testThreshold)
		}

		q, err = fake.EmbedQuery(ctx, "receta de pasta")
		if err != nil {
			t.Fatal(err)
		}
		if got, sim, ok := r.Detect(ctx, q); ok {
			t.Errorf("Detect(receta de pasta) = %+v, %v, true; se esperaba ok=false", got, sim)
		}

		// Una caché cargada desde la base de datos también sirve.
		r2 := NewResolver(tc.Pool, fake, testThreshold)
		if err := r2.Load(ctx); err != nil {
			t.Fatal(err)
		}
		q, _ = fake.EmbedQuery(ctx, "mi coche")
		if got, _, ok := r2.Detect(ctx, q); !ok || got != res[1].Topic {
			t.Errorf("tras Load, Detect(mi coche) = %+v, %v; se esperaba %+v", got, ok, res[1].Topic)
		}
	})

	run("Ollama no disponible: NULL y FillPending", func(t *testing.T, ctx context.Context) {
		down := NewResolver(tc.Pool, unavailableEmbedder{dims: testDims}, testThreshold)
		got, err := down.Resolve(ctx, []string{"Entrenamiento"})
		if err != nil {
			t.Fatalf("Resolve con Ollama caído falló: %v", err)
		}
		if len(got) != 1 || got[0].Topic.Slug != "entrenamiento" {
			t.Fatalf("resolución inesperada: %+v", got)
		}
		if n := countPending(t, ctx); n != 1 {
			t.Fatalf("hay %d temas sin embedding, se esperaba 1", n)
		}

		// Sin Ollama, FillPending devuelve el error y no rellena nada.
		if n, err := down.FillPending(ctx, 10); err == nil || n != 0 {
			t.Errorf("FillPending con Ollama caído = %d, %v; se esperaba 0 y error", n, err)
		}

		r := NewResolver(tc.Pool, fake, testThreshold)
		n, err := r.FillPending(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("FillPending rellenó %d temas, se esperaba 1", n)
		}
		if n := countPending(t, ctx); n != 0 {
			t.Errorf("quedan %d temas sin embedding, se esperaba 0", n)
		}
		// La caché se actualiza al rellenar.
		q, _ := fake.EmbedQuery(ctx, "gym")
		if got, _, ok := r.Detect(ctx, q); !ok || got.Slug != "entrenamiento" {
			t.Errorf("tras FillPending, Detect no encontró el tema: %+v, %v", got, ok)
		}

		if n, err := r.FillPending(ctx, 10); err != nil || n != 0 {
			t.Errorf("segundo FillPending = %d, %v; se esperaba 0 y sin error", n, err)
		}
	})

	run("FillPending respeta el límite", func(t *testing.T, ctx context.Context) {
		down := NewResolver(tc.Pool, unavailableEmbedder{dims: testDims}, testThreshold)
		if _, err := down.Resolve(ctx, []string{"uno dos", "tres cuatro", "cinco seis"}); err != nil {
			t.Fatal(err)
		}
		r := NewResolver(tc.Pool, fake, testThreshold)
		n, err := r.FillPending(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("FillPending(2) rellenó %d, se esperaba 2", n)
		}
		if p := countPending(t, ctx); p != 1 {
			t.Errorf("quedan %d pendientes, se esperaba 1", p)
		}
	})
}
