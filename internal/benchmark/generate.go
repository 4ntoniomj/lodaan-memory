package benchmark

import (
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/pgvector/pgvector-go"
)

const (
	// noiseSigma is the standard deviation of the gaussian noise added to each component of a
	// unit-norm centroid before normalizing the row again.
	noiseSigma = 0.035
	// zipfS is the exponent of the Zipf distribution used to assign topics.
	zipfS = 1.1
	// exactTokenRate is the fraction of rows that carry a plate-like token (1234-ABC).
	exactTokenRate = 0.01
	// exactQueryRate is the fraction of text queries that look up an exact token.
	exactQueryRate = 0.10
	// historyYears is how far back occurred_at goes.
	historyYears = 5
	// maxExactTokens caps how many exact tokens the generator remembers for building queries.
	maxExactTokens = 1000
)

// Independent random streams derived from the seed, so that changing one part of the data
// (e.g. the number of topics) does not alter the others.
const (
	streamCentroids uint64 = iota + 1
	streamTopics
	streamRows
	streamQueries
)

// memoryKinds are the values of the memory_kind enum used for the synthetic rows.
var memoryKinds = []string{"fact", "preference", "decision", "event", "note"}

// newRand returns a deterministic generator for (seed, stream).
func newRand(seed int64, stream uint64) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(seed), stream))
}

// row is one synthetic memory.
type row struct {
	kind       string
	title      string
	content    string
	hash       [sha256.Size]byte
	occurredAt time.Time
	// vec is reused by the generator: it is only valid until the next call to nextRow.
	vec []float32
	// topic is the index (0-based) of the topic of the row, following a Zipf distribution.
	topic int
}

// generator produces deterministic synthetic rows: the same seed gives the same sequence.
type generator struct {
	seed      int64
	dims      int
	now       time.Time
	centroids [][]float32
	rng       *rand.Rand
	zipf      *rand.Zipf
	vecBuf    []float32
	// exactTokens are the first plate-like tokens written into rows, used later to query them.
	exactTokens []string
}

// newGenerator creates a generator with the given number of cluster centroids and topics.
// now anchors occurred_at (the last historyYears years before it).
func newGenerator(seed int64, dims, clusters, topics int, now time.Time) *generator {
	crng := newRand(seed, streamCentroids)
	centroids := make([][]float32, clusters)
	for i := range centroids {
		centroids[i] = randomUnit(crng, dims)
	}
	rng := newRand(seed, streamRows)
	return &generator{
		seed:      seed,
		dims:      dims,
		now:       now,
		centroids: centroids,
		rng:       rng,
		zipf:      rand.NewZipf(rng, zipfS, 1, uint64(max(topics, 1)-1)),
	}
}

// randomUnit returns a random unit-norm vector (normalized gaussian components).
func randomUnit(rng *rand.Rand, dims int) []float32 {
	v := make([]float32, dims)
	var sum float64
	for i := range v {
		x := rng.NormFloat64()
		v[i] = float32(x)
		sum += x * x
	}
	normalize(v, sum)
	return v
}

// normalize scales v to unit norm given the sum of the squares of its (unrounded) components.
func normalize(v []float32, sumSquares float64) {
	if sumSquares == 0 {
		return
	}
	inv := 1 / math.Sqrt(sumSquares)
	for i := range v {
		v[i] = float32(float64(v[i]) * inv)
	}
}

// noisyVector writes into out (reallocated if needed) centroid plus gaussian noise of
// standard deviation noiseSigma per component, normalized to unit norm, and returns it.
func noisyVector(rng *rand.Rand, centroid, out []float32) []float32 {
	if len(out) != len(centroid) {
		out = make([]float32, len(centroid))
	}
	var sum float64
	for i, c := range centroid {
		x := float64(c) + noiseSigma*rng.NormFloat64()
		out[i] = float32(x)
		sum += x * x
	}
	normalize(out, sum)
	return out
}

// pickWords returns n words of the vocabulary chosen uniformly at random.
func pickWords(rng *rand.Rand, n int) []string {
	words := make([]string, n)
	for i := range words {
		words[i] = vocabulary[rng.IntN(len(vocabulary))]
	}
	return words
}

// exactToken builds a plate-like token such as "1234-ABC".
func exactToken(rng *rand.Rand) string {
	return fmt.Sprintf("%04d-%c%c%c", rng.IntN(10000),
		'A'+rune(rng.IntN(26)), 'A'+rune(rng.IntN(26)), 'A'+rune(rng.IntN(26)))
}

// nextRow generates the next synthetic row.
func (g *generator) nextRow() row {
	r := g.rng

	g.vecBuf = noisyVector(r, g.centroids[r.IntN(len(g.centroids))], g.vecBuf)

	title := strings.Join(pickWords(r, 4+r.IntN(5)), " ")

	words := pickWords(r, 15+r.IntN(46))
	if r.Float64() < exactTokenRate {
		tok := exactToken(r)
		if len(g.exactTokens) < maxExactTokens {
			g.exactTokens = append(g.exactTokens, tok)
		}
		pos := r.IntN(len(words) + 1)
		words = append(words, "")
		copy(words[pos+1:], words[pos:])
		words[pos] = tok
	}
	content := strings.Join(words, " ")

	span := int64(historyYears * 365 * 24 * time.Hour)
	occurred := g.now.Add(-time.Duration(r.Int64N(span))).Truncate(time.Second)

	return row{
		kind:       memoryKinds[r.IntN(len(memoryKinds))],
		title:      title,
		content:    content,
		hash:       sha256.Sum256([]byte(content)),
		occurredAt: occurred,
		vec:        g.vecBuf,
		topic:      int(g.zipf.Uint64()),
	}
}

// topicData returns the slugs (tema-0001, ...) and embeddings of n topics; each embedding is
// the centroid of a randomly chosen cluster. The vectors share memory with the centroids.
func (g *generator) topicData(n int) (slugs []string, vecs [][]float32) {
	rng := newRand(g.seed, streamTopics)
	slugs = make([]string, n)
	vecs = make([][]float32, n)
	for i := range slugs {
		slugs[i] = fmt.Sprintf("tema-%04d", i+1)
		vecs[i] = g.centroids[rng.IntN(len(g.centroids))]
	}
	return slugs, vecs
}

// querySet holds the test queries of a benchmark. It is the same for every scale.
type querySet struct {
	// vecs are the query vectors (new points around random centroids).
	vecs []pgvector.HalfVector
	// texts are the full-text queries: 2 distinct vocabulary words, 10 % exact tokens.
	texts []string
	// topics are the topic ids for the topic-card query (Zipf-distributed, like the data).
	topics []int64
}

// newQuerySet builds n queries. topicIDs are the database ids of the topics; exact are the
// exact tokens present in the loaded data (may be empty). It only depends on the seed and
// its arguments, never on the state of the row generator.
func newQuerySet(g *generator, n int, topicIDs []int64, exact []string) querySet {
	rng := newRand(g.seed, streamQueries)
	zipf := rand.NewZipf(rng, zipfS, 1, uint64(max(len(topicIDs), 1)-1))

	qs := querySet{
		vecs:   make([]pgvector.HalfVector, n),
		texts:  make([]string, n),
		topics: make([]int64, n),
	}
	for i := 0; i < n; i++ {
		v := noisyVector(rng, g.centroids[rng.IntN(len(g.centroids))], nil)
		qs.vecs[i] = pgvector.NewHalfVector(v)

		if len(exact) > 0 && rng.Float64() < exactQueryRate {
			qs.texts[i] = exact[rng.IntN(len(exact))]
		} else {
			// Two distinct words: websearch_to_tsquery combines them with AND.
			w1 := rng.IntN(len(vocabulary))
			w2 := rng.IntN(len(vocabulary) - 1)
			if w2 >= w1 {
				w2++
			}
			qs.texts[i] = vocabulary[w1] + " " + vocabulary[w2]
		}

		if len(topicIDs) > 0 {
			qs.topics[i] = topicIDs[zipf.Uint64()]
		}
	}
	return qs
}
