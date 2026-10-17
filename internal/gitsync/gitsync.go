// Package gitsync exports the local changes of the lodan memory to encrypted batch files
// inside a git working copy and imports the files written by other machines.
//
// Each machine writes only to changes/<machine-id>/, so git never has to merge a file. The
// files are JSON Lines encrypted with age (X25519). Records travel by their global sync_id;
// local numeric ids never leave the machine. Embeddings are not exported: imported records
// arrive without them and the maintenance worker computes them.
package gitsync

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Format is the version of the batch file format, written in each file's header.
const Format = "lodan-sync/1"

// ExportOptions configures Export.
type ExportOptions struct {
	Dir       string // root of the repository working copy
	MachineID string
	Recipient string // age X25519 recipient, "age1..."
}

// ExportResult describes the file written by Export.
type ExportResult struct {
	// File is the path relative to Dir, with forward slashes; "" when nothing changed.
	File      string
	Memories  int
	Relations int
	Sessions  int
	Deletions int
}

// ImportOptions configures Import.
type ImportOptions struct {
	Dir       string
	MachineID string // own machine: its folder is skipped
	Identity  string // age X25519 identity, "AGE-SECRET-KEY-1..."
}

// ImportResult describes what Import applied.
type ImportResult struct {
	Files          int             // files applied in this run
	Contradictions []Contradiction // active records with the same key found in this run
	Errors         []string        // per-file errors; those files are retried next time
}

// Contradiction is a pair of active records that had the same key on two machines. The most
// recent keeps the key; both are related as contradicts and the user decides which is true.
type Contradiction struct {
	Key     string
	KeptID  int64 // local id of the record that keeps the key
	OtherID int64 // local id of the record left active without key
}

// Line types of a batch file.
const (
	typeHeader   = "header"
	typeSession  = "session"
	typeMemory   = "memory"
	typeRelation = "relation"
	typeDeletion = "deletion"
)

type headerLine struct {
	Type      string    `json:"type"`
	Format    string    `json:"format"`
	Machine   string    `json:"machine"`
	CreatedAt time.Time `json:"created_at"`
}

type sessionLine struct {
	Type           string     `json:"type"`
	ID             string     `json:"id"` // ULID, already global
	Client         string     `json:"client"`
	StartedAt      time.Time  `json:"started_at"`
	LastActivityAt time.Time  `json:"last_activity_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	Topics         []string   `json:"topics,omitempty"`
}

type memoryLine struct {
	Type       string    `json:"type"`
	SyncID     string    `json:"sync_id"`
	Rev        string    `json:"rev"`
	Base       string    `json:"base,omitempty"` // last version exchanged before this one
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Title      string    `json:"title"`
	Content    string    `json:"content"`
	Key        string    `json:"key,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Topics     []string  `json:"topics,omitempty"`
}

type relationLine struct {
	Type      string    `json:"type"`
	Source    string    `json:"source"` // sync_id
	Target    string    `json:"target"` // sync_id
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type deletionLine struct {
	Type      string    `json:"type"`
	SyncID    string    `json:"sync_id"`
	DeletedAt time.Time `json:"deleted_at"`
}

// batch is the decoded content of one file.
type batch struct {
	header    headerLine
	sessions  []sessionLine
	memories  []memoryLine
	relations []relationLine
	deletions []deletionLine
}

// decodeBatch parses the JSON Lines of a decrypted file.
func decodeBatch(lines [][]byte) (batch, error) {
	var b batch
	for i, raw := range lines {
		if len(raw) == 0 {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return b, fmt.Errorf("línea %d: %w", i+1, err)
		}
		var err error
		switch probe.Type {
		case typeHeader:
			err = json.Unmarshal(raw, &b.header)
		case typeSession:
			var l sessionLine
			if err = json.Unmarshal(raw, &l); err == nil {
				b.sessions = append(b.sessions, l)
			}
		case typeMemory:
			var l memoryLine
			if err = json.Unmarshal(raw, &l); err == nil {
				b.memories = append(b.memories, l)
			}
		case typeRelation:
			var l relationLine
			if err = json.Unmarshal(raw, &l); err == nil {
				b.relations = append(b.relations, l)
			}
		case typeDeletion:
			var l deletionLine
			if err = json.Unmarshal(raw, &l); err == nil {
				b.deletions = append(b.deletions, l)
			}
		default:
			return b, fmt.Errorf("línea %d: tipo desconocido %q", i+1, probe.Type)
		}
		if err != nil {
			return b, fmt.Errorf("línea %d: %w", i+1, err)
		}
	}
	if b.header.Format != Format {
		return b, fmt.Errorf("formato %q no soportado (se esperaba %s)", b.header.Format, Format)
	}
	return b, nil
}

// fileNameRe matches a batch file name: <UTC timestamp>-<n>.jsonl.age.
var fileNameRe = regexp.MustCompile(`^(\d{8}T\d{6}Z)-(\d+)\.jsonl\.age$`)

// fileName builds the name of the n-th batch file written at t.
func fileName(t time.Time, n int64) string {
	return t.UTC().Format("20060102T150405Z") + "-" + strconv.FormatInt(n, 10) + ".jsonl.age"
}

// parseFileName returns the ordering keys of a batch file name.
func parseFileName(name string) (stamp string, n int64, ok bool) {
	m := fileNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}

// validMachineID reports whether id can name a folder on every operating system.
var machineIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func checkMachineID(id string) error {
	if !machineIDRe.MatchString(id) {
		return fmt.Errorf("identificador de máquina no válido %q: usa letras, números, punto, guion o guion bajo", id)
	}
	return nil
}
