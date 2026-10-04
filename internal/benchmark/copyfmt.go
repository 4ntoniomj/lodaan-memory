package benchmark

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// appendCopyText appends s to buf escaped for the PostgreSQL COPY text format: backslash,
// newline, carriage return and tab are written as \\, \n, \r and \t.
func appendCopyText(buf []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		case '\t':
			buf = append(buf, '\\', 't')
		default:
			buf = append(buf, c)
		}
	}
	return buf
}

// appendBytea appends b as a bytea in COPY text format: \\x<hex> (the backslash of the bytea
// hex form is itself escaped by the COPY text format).
func appendBytea(buf []byte, b []byte) []byte {
	buf = append(buf, '\\', '\\', 'x')
	return hex.AppendEncode(buf, b)
}

// appendVector appends v as a halfvec literal, [0.1,0.2,...]. Four significant digits are
// enough: halfvec keeps around three and a half, and it makes the COPY stream much smaller.
func appendVector(buf []byte, v []float32) []byte {
	buf = append(buf, '[')
	for i, x := range v {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendFloat(buf, float64(x), 'g', 4, 32)
	}
	return append(buf, ']')
}

// appendTimestamp appends t (UTC) as a timestamptz literal.
func appendTimestamp(buf []byte, t time.Time) []byte {
	buf = t.UTC().AppendFormat(buf, "2006-01-02 15:04:05")
	return append(buf, '+', '0', '0')
}

// copyIn runs a COPY ... FROM STDIN (text format) on conn. produce writes the lines to w, from
// a goroutine connected through an io.Pipe. It returns the number of rows the server copied.
//
// halfvec has no registered pgx codec, so pgx.CopyFrom (binary) cannot be used: the text format
// lets PostgreSQL parse the vectors itself.
func copyIn(ctx context.Context, conn *pgx.Conn, copySQL string, produce func(w *bufio.Writer) error) (int64, error) {
	pr, pw := io.Pipe()
	prodErr := make(chan error, 1)
	go func() {
		bw := bufio.NewWriterSize(pw, 1<<20)
		err := produce(bw)
		if err == nil {
			err = bw.Flush()
		}
		// A nil error closes the pipe with io.EOF, which ends the COPY.
		_ = pw.CloseWithError(err)
		prodErr <- err
	}()

	tag, err := conn.PgConn().CopyFrom(ctx, pr, copySQL)
	// If CopyFrom stopped early (server error), unblock the producer.
	// Its pending writes then fail with io.ErrClosedPipe.
	_ = pr.Close()
	perr := <-prodErr

	// The producer error is the root cause when both fail (CopyFrom then only reports
	// the COPY that was aborted because of it).
	if perr != nil && !errors.Is(perr, io.ErrClosedPipe) {
		return 0, perr
	}
	if err != nil {
		return 0, fmt.Errorf("falló el COPY: %w", err)
	}
	return tag.RowsAffected(), nil
}
