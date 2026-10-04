package install

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lodan/internal/config"
	"lodan/internal/database"
	"lodan/internal/embedding"
)

// databaseName is the PostgreSQL database lodan stores its data in.
const databaseName = "lodan"

// minPgvector is the oldest pgvector that lodan supports (halfvec needs 0.7.0).
const minPgvector = "0.7.0"

// pgRuntime is the private PostgreSQL runtime (micromamba plus the conda
// environment). Runtime implements it; tests use fakes.
type pgRuntime interface {
	MissingPostgresFiles() []string
	PostgresBinDir() string
	EnsurePostgres(ctx context.Context, log func(string)) (string, error)
}

var _ pgRuntime = Runtime{}

// ollamaAPI groups everything the installer does with Ollama. realOllama
// implements it on top of the functions of ollama.go; tests use fakes.
type ollamaAPI interface {
	Detect(ctx context.Context, baseURL string) (version string, ok bool)
	HasModel(ctx context.Context, baseURL, model string) (bool, error)
	Pull(ctx context.Context, baseURL, model string, progress func(status string, done, total int64)) error
	Install(ctx context.Context, dir string, progress func(done, total int64)) (exe string, err error)
	Find(dir string) (string, error)
	Env(dir string) map[string]string
	// Embed computes a test embedding with the configured model and checks its dimensions.
	Embed(ctx context.Context, cfg config.Config) error
}

type realOllama struct{}

func (realOllama) Detect(ctx context.Context, baseURL string) (string, bool) {
	return Detect(ctx, baseURL)
}

func (realOllama) HasModel(ctx context.Context, baseURL, model string) (bool, error) {
	return HasModel(ctx, baseURL, model)
}

func (realOllama) Pull(ctx context.Context, baseURL, model string, progress func(string, int64, int64)) error {
	return Pull(ctx, baseURL, model, progress)
}

func (realOllama) Install(ctx context.Context, dir string, progress func(int64, int64)) (string, error) {
	return InstallOllama(ctx, dir, progress)
}

func (realOllama) Find(dir string) (string, error) { return FindOllama(dir) }

func (realOllama) Env(dir string) map[string]string { return OllamaEnv(dir) }

func (realOllama) Embed(ctx context.Context, cfg config.Config) error {
	emb, err := embedding.NewOllama(embedding.Options{
		BaseURL:   cfg.OllamaURL,
		Model:     cfg.EmbedModel,
		Dims:      cfg.EmbedDims,
		KeepAlive: cfg.EmbedKeepAlive,
		// The first embedding loads the model, which can be slow on CPU.
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		return err
	}
	_, err = emb.EmbedQuery(ctx, "prueba de lodan doctor")
	return err
}

// clusterAPI is the part of the PostgreSQL cluster that install, uninstall and
// doctor use. realCluster implements it; tests use fakes.
type clusterAPI interface {
	Lock(ctx context.Context) (unlock func(), err error)
	Initialized() bool
	Init(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Status(ctx context.Context) (running bool, err error)
	EnsureDatabase(ctx context.Context, name string) error
	// Migrate applies the pending migrations and reports whether it applied any.
	Migrate(ctx context.Context) (changed bool, err error)
	// VectorVersion returns the version of the pgvector extension of the lodan
	// database, which also proves that the connection works.
	VectorVersion(ctx context.Context) (string, error)
}

// realCluster adds Migrate and VectorVersion to *database.Cluster.
type realCluster struct {
	*database.Cluster
	cfg config.Config
}

func newRealCluster(cfg config.Config) (clusterAPI, error) {
	cl, err := database.NewCluster(cfg)
	if err != nil {
		return nil, err
	}
	return realCluster{Cluster: cl, cfg: cfg}, nil
}

func (c realCluster) Migrate(ctx context.Context) (bool, error) {
	dsn, err := c.DSN(databaseName)
	if err != nil {
		return false, err
	}
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		return false, err
	}
	defer pool.Close()

	before, err := countMigrations(ctx, pool)
	if err != nil {
		return false, err
	}
	if err := database.Migrate(ctx, pool, database.Settings{Dims: c.cfg.EmbedDims, Model: c.cfg.EmbedModel}); err != nil {
		return false, err
	}
	after, err := countMigrations(ctx, pool)
	if err != nil {
		return false, err
	}
	return after > before, nil
}

// countMigrations returns how many migrations are recorded (0 if the table does not exist yet).
func countMigrations(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return 0, fmt.Errorf("no se pudo comprobar schema_migrations: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		return 0, fmt.Errorf("no se pudo contar las migraciones aplicadas: %w", err)
	}
	return n, nil
}

func (c realCluster) VectorVersion(ctx context.Context) (string, error) {
	dsn, err := c.DSN(databaseName)
	if err != nil {
		return "", err
	}
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer pool.Close()

	var version string
	err = pool.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'vector'").Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("la extensión vector no está creada en la base de datos")
	}
	if err != nil {
		return "", fmt.Errorf("no se pudo consultar la versión de pgvector: %w", err)
	}
	return version, nil
}

// versionAtLeast reports whether the dotted version v (for example "0.8.6") is
// at least minimum. Non numeric suffixes of a component are ignored.
func versionAtLeast(v, minimum string) bool {
	a, b := versionParts(v), versionParts(minimum)
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return true
}

func versionParts(v string) []int {
	var parts []int
	for _, f := range strings.Split(strings.TrimSpace(v), ".") {
		digits := f
		for i, r := range f {
			if r < '0' || r > '9' {
				digits = f[:i]
				break
			}
		}
		n, _ := strconv.Atoi(digits)
		parts = append(parts, n)
	}
	return parts
}
