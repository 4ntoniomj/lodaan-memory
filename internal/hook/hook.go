// Package hook implements the lifecycle hooks that AI clients run without depending on the
// AI's discipline: recovery instructions after a context compaction, and passive capture of
// the "## Key Learnings" block a subagent leaves when it finishes.
//
// Hooks never block the client: callers ignore their errors and always exit with code 0.
package hook

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"lodan/internal/memory"
	"lodan/internal/session"
)

// RecoveryInstructions is what the AI receives right after a compaction.
const RecoveryInstructions = "lodan: el contexto se acaba de compactar. Antes de seguir, llama una vez a recall de lodan en lote (queries) con los temas en curso de esta conversación y continúa con lo que devuelva."

// StartInstructions is what the AI receives when a conversation starts in a client that has
// no compaction event (Antigravity).
const StartInstructions = "lodan: conversación nueva. Antes de responder sobre un tema, llama una vez a recall de lodan en lote (queries) con los temas de la conversación."

// Limits of what the hooks store.
const (
	maxSummaryRunes = 2000 // same cap as session.Tracker.End
	maxItems        = 5
	minItemRunes    = 20
	minItemWords    = 4
	maxTitleRunes   = 80
	maxInputBytes   = 16 << 20
)

// Deps are the lodan services the hooks use. A nil *Deps means the database is unavailable.
type Deps struct {
	Pool     *pgxpool.Pool
	Memory   *memory.Service
	Sessions *session.Manager
}

// event holds the fields of the client's hook input that lodan uses.
type event struct {
	HookEventName        string `json:"hook_event_name"`
	Cwd                  string `json:"cwd"`
	CompactSummary       string `json:"compact_summary"`
	LastAssistantMessage string `json:"last_assistant_message"`
	AgentTranscriptPath  string `json:"agent_transcript_path"`
	ToolName             string `json:"tool_name"`
	ToolInput            struct {
		Message string `json:"message"`
	} `json:"tool_input"`
}

// readEvent decodes the hook input; empty or invalid input gives an empty event.
func readEvent(in io.Reader) event {
	var ev event
	data, err := io.ReadAll(io.LimitReader(in, maxInputBytes))
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return ev
	}
	_ = json.Unmarshal(data, &ev)
	return ev
}

// Compact handles the compaction hook. After PostCompact with a summary it stores the summary
// in the client's session; for any other event it writes the recovery instructions to out,
// followed by the summary of the client's last ended session.
func Compact(ctx context.Context, deps *Deps, client string, in io.Reader, out io.Writer) error {
	ev := readEvent(in)
	if ev.HookEventName == "PostCompact" && strings.TrimSpace(ev.CompactSummary) != "" {
		if deps == nil {
			return nil
		}
		return storeSummary(ctx, deps.Pool, client, ev.CompactSummary)
	}

	if _, err := fmt.Fprintln(out, RecoveryInstructions); err != nil {
		return err
	}
	line, err := LastSessionLine(ctx, deps, client)
	if err != nil || line == "" {
		return err
	}
	_, err = fmt.Fprintln(out, line)
	return err
}

// LastSessionLine returns "Última sesión de lodan (<client>, <date>): <summary>" for the
// client's most recently ended session with a summary, or "" when there is none (or no
// database).
func LastSessionLine(ctx context.Context, deps *Deps, client string) (string, error) {
	if deps == nil {
		return "", nil
	}
	var ended time.Time
	var summary string
	err := deps.Pool.QueryRow(ctx, `SELECT ended_at, summary FROM sessions
		WHERE client = $1 AND ended_at IS NOT NULL AND coalesce(summary, '') <> ''
		ORDER BY ended_at DESC LIMIT 1`, client).Scan(&ended, &summary)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("no se pudo leer la última sesión: %w", err)
	}
	return fmt.Sprintf("Última sesión de lodan (%s, %s): %s", client, ended.Local().Format("2006-01-02"),
		strings.ReplaceAll(summary, "\n", " ")), nil
}

// SessionStart brings the other machines' changes when a session starts. pull is nil when
// sync is not configured. With format "antigravity" it writes the empty JSON object that
// Antigravity expects; with "text" it writes nothing. A pull error is returned after the
// output is written, so the client always gets a valid answer.
func SessionStart(ctx context.Context, pull func(context.Context) error, format string, out io.Writer) error {
	var err error
	if pull != nil {
		err = pull(ctx)
	}
	if format == "antigravity" {
		if _, werr := fmt.Fprintln(out, "{}"); werr != nil && err == nil {
			err = werr
		}
	}
	return err
}

// storeSummary saves the compaction summary in the client's most recently active open
// session, without closing it; with no open session it creates an ended one.
func storeSummary(ctx context.Context, pool *pgxpool.Pool, client, summary string) error {
	summary = truncateRunes(strings.TrimSpace(summary), maxSummaryRunes)
	tag, err := pool.Exec(ctx, `UPDATE sessions SET summary = $2
		WHERE id = (SELECT id FROM sessions WHERE client = $1 AND ended_at IS NULL
		            ORDER BY last_activity_at DESC LIMIT 1)`, client, summary)
	if err != nil {
		return fmt.Errorf("no se pudo guardar el resumen de la compactación: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions (id, client, ended_at, summary) VALUES ($1, $2, now(), $3)`,
		ulid.Make().String(), client, summary); err != nil {
		return fmt.Errorf("no se pudo guardar el resumen de la compactación: %w", err)
	}
	return nil
}

// SubagentStop saves each item of the subagent's "## Key Learnings" block as a note in the
// topic of the project. It returns how many new records were saved (duplicates of an active
// record do not count).
func SubagentStop(ctx context.Context, deps *Deps, client string, in io.Reader) (int, error) {
	ev := readEvent(in)
	if deps == nil {
		return 0, nil
	}
	items := ExtractKeyLearnings(subagentMessage(ev))
	if len(items) == 0 {
		return 0, nil
	}
	var topics []string
	if ev.Cwd != "" {
		topics = []string{ProjectTopic(ev.Cwd)}
	}
	batch := make([]memory.Item, len(items))
	for i, it := range items {
		batch[i] = memory.Item{Title: title(it), Content: it, Kind: memory.KindNote, Topics: topics}
	}
	saved, err := deps.Memory.Remember(ctx, deps.Sessions.NewTracker(client), batch)
	if err != nil {
		return 0, fmt.Errorf("no se pudieron guardar los aprendizajes: %w", err)
	}
	n := 0
	for _, s := range saved {
		if !s.Duplicate {
			n++
		}
	}
	return n, nil
}

// subagentMessage picks the subagent's report: the SubagentHandback message of a PostToolUse
// event, else the final message, else the last SubagentHandback in the subagent's transcript.
func subagentMessage(ev event) string {
	if ev.ToolInput.Message != "" {
		return ev.ToolInput.Message
	}
	if hasBlock(ev.LastAssistantMessage) || ev.AgentTranscriptPath == "" {
		return ev.LastAssistantMessage
	}
	if msg := lastHandback(ev.AgentTranscriptPath); msg != "" {
		return msg
	}
	return ev.LastAssistantMessage
}

// lastHandback returns the message of the last SubagentHandback tool call in a transcript.
func lastHandback(path string) string {
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var last string
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, []byte("SubagentHandback")) {
			var entry struct {
				Message struct {
					Content []struct {
						Type  string `json:"type"`
						Name  string `json:"name"`
						Input struct {
							Message string `json:"message"`
						} `json:"input"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) == nil {
				for _, c := range entry.Message.Content {
					if c.Type == "tool_use" && c.Name == "SubagentHandback" && c.Input.Message != "" {
						last = c.Input.Message
					}
				}
			}
		}
		if err != nil {
			return last
		}
	}
}

var (
	headingRe = regexp.MustCompile(`^##\s+Key Learnings\s*$`)
	itemRe    = regexp.MustCompile(`^\s*\d+[.)]\s+(.*\S)\s*$`)
	secretRes = []*regexp.Regexp{
		regexp.MustCompile(`AGE-SECRET-KEY-`),
		regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)(password|contraseña|token|secret|api[ _]?key)\s*[:=]\s*\S`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9]{16,}`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`(?i)(password|passwd|contraseña)[^.\n]{0,40}\s(is|es|was|era)\s+\S`), // "la contraseña de X es Y"
		regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),                                        // AWS access key
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.`),                        // JWT
		regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{20,}`),
		regexp.MustCompile(`\b\d{8}-?[A-Za-z]\b`),           // DNI, also 12345678-Z
		regexp.MustCompile(`\b[XYZxyz]-?\d{7}-?[A-Za-z]\b`), // NIE
	}
)

func hasBlock(msg string) bool {
	for _, line := range strings.Split(msg, "\n") {
		if headingRe.MatchString(strings.TrimRight(line, "\r")) {
			return true
		}
	}
	return false
}

// ExtractKeyLearnings returns the items (at most 5) of the last "## Key Learnings" block of
// msg. Items that are too short, or that look like secrets or identity numbers, are dropped.
func ExtractKeyLearnings(msg string) []string {
	lines := strings.Split(msg, "\n")
	start := -1
	for i, line := range lines {
		if headingRe.MatchString(strings.TrimRight(line, "\r")) {
			start = i
		}
	}
	if start < 0 {
		return []string{}
	}
	var items []string
	for _, line := range lines[start+1:] {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "#") {
			break
		}
		m := itemRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if len(items) == maxItems {
			break
		}
		items = append(items, m[1])
	}
	out := []string{}
	for _, it := range items {
		if utf8.RuneCountInString(it) < minItemRunes || len(strings.Fields(it)) < minItemWords || looksSecret(it) {
			continue
		}
		out = append(out, it)
	}
	return out
}

func looksSecret(s string) bool {
	for _, re := range secretRes {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// ProjectTopic is the name of the git repository root folder when cwd is inside a working
// copy, else the name of cwd.
func ProjectTopic(cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return filepath.Base(filepath.FromSlash(root))
		}
	}
	return filepath.Base(filepath.Clean(cwd))
}

// title is the item cut to 80 characters at a word boundary, with "…" when cut.
func title(item string) string {
	runes := []rune(item)
	if len(runes) <= maxTitleRunes {
		return item
	}
	cut := maxTitleRunes
	for i := maxTitleRunes; i > 0; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(runes[:cut]), " ") + "…"
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
