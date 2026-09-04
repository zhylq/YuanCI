package runner

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
)

// Each pipe has its own redactor. Retain only a possible secret prefix, never
// an entire line. At EOF even a partial secret prefix is suppressed.
type redactingWriter struct {
	target  io.Writer
	secrets [][]byte
	pending []byte
	closed  bool
}

func newRedactingWriter(target io.Writer, secrets [][]byte) (*redactingWriter, error) {
	if target == nil {
		target = io.Discard
	}
	w := &redactingWriter{target: target}
	total := 0
	if len(secrets) > 64 {
		return nil, errors.New("too many redaction values")
	}
	for _, secret := range secrets {
		total += len(secret)
		if len(secret) == 0 || len(secret) > 8192 || total > 65536 {
			w.destroy()
			return nil, errors.New("invalid redaction value bounds")
		}
		w.secrets = append(w.secrets, append([]byte(nil), secret...))
	}
	return w, nil
}
func (w *redactingWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	output := make([]byte, 0, 32768)
	defer clear(output[:cap(output)])
	for i, b := range p {
		w.pending = append(w.pending, b)
		output = w.drain(output, false)
		if len(output) >= 16384 {
			if err := writeRedacted(w.target, output); err != nil {
				return i + 1, err
			}
			clear(output)
			output = output[:0]
		}
	}
	if err := writeRedacted(w.target, output); err != nil {
		return len(p), err
	}
	return len(p), nil
}
func (w *redactingWriter) drain(out []byte, final bool) []byte {
	for len(w.pending) > 0 {
		match := 0
		prefix := false
		for _, s := range w.secrets {
			if len(w.pending) < len(s) && bytes.HasPrefix(s, w.pending) {
				prefix = true
			}
			if len(w.pending) >= len(s) && bytes.HasPrefix(w.pending, s) {
				match = max(match, len(s))
			}
		}
		if prefix && !final {
			return out
		}
		if match > 0 {
			out = append(out, "[REDACTED]"...)
			w.consume(match)
		} else if prefix {
			out = append(out, "[REDACTED]"...)
			w.consume(len(w.pending))
		} else {
			out = append(out, w.pending[0])
			w.consume(1)
		}
	}
	return out
}
func (w *redactingWriter) consume(n int) {
	remaining := copy(w.pending, w.pending[n:])
	clear(w.pending[remaining:])
	w.pending = w.pending[:remaining]
}
func (w *redactingWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	defer w.destroy()
	output := w.drain(nil, true)
	defer clear(output)
	return writeRedacted(w.target, output)
}
func (w *redactingWriter) destroy() {
	for _, s := range w.secrets {
		clear(s)
	}
	clear(w.pending)
	w.secrets = nil
	w.pending = nil
}
func writeRedacted(target io.Writer, p []byte) error {
	if len(p) == 0 {
		return nil
	}
	n, err := target.Write(p)
	if err == nil && n != len(p) {
		return io.ErrShortWrite
	}
	return err
}

func checkoutRedactionValues(source *localSource) [][]byte {
	if source == nil {
		return nil
	}
	token := source.credential
	basic := append([]byte("x-access-token:"), token...)
	defer clear(basic)
	return [][]byte{append([]byte(nil), token...), []byte(base64.StdEncoding.EncodeToString(token)), []byte(base64.StdEncoding.EncodeToString(basic)), []byte(url.QueryEscape(string(token)))}
}
