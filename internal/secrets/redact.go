package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Redactor replaces known secret values with "***" in anything tink prints.
//
// It exists for the leaks tink does not control: an Incus driver that echoes a credential in an
// error, a wrapped command line, a diff. Every value tink decrypts is Added, so wherever it later
// turns up in output, it is scrubbed. It is the last line of defense, not the first: tink also
// avoids putting values in output in the first place.
//
// It matches the value as written and in the encodings it plausibly takes in output: base64
// (standard and URL, padded and not), hex, URL- and path-escaped, Go-quoted (what %q prints) and
// JSON-escaped. A multi-line value (a PEM key) also has each of its lines redacted on its own.
//
// This is BEST EFFORT, not a guarantee. It cannot catch a secret that something has transformed
// beyond those forms (hashed, split across fields, YAML-quoted), a value tink never decrypted (a
// credential held only by Incus, such as a storage pool's API key), or output tink does not
// print. It is a net under the places tink avoids printing a secret, not a substitute for them.
// Values shorter than MinLength are not added, since replacing them would mangle unrelated text.
type Redactor struct {
	mu     sync.RWMutex
	needle map[string]struct{}
	repl   *strings.Replacer
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return &Redactor{needle: map[string]struct{}{}} }

// Add registers a secret value (and its encodings) for redaction.
func (r *Redactor) Add(value string) {
	if len(value) < MinLength {
		return
	}
	forms := []string{value}
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); len(line) >= MinLength {
			forms = append(forms, line)
		}
	}
	for _, f := range append([]string(nil), forms...) {
		b := []byte(f)
		forms = append(forms,
			base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
			base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
			url.QueryEscape(f), url.PathEscape(f),
		)
		if q := strconv.Quote(f); len(q) > 2 {
			forms = append(forms, q[1:len(q)-1])
		}
		if j, err := json.Marshal(f); err == nil && len(j) > 2 {
			forms = append(forms, string(j[1:len(j)-1])) // JSON escapes <, >, & and non-ASCII
		}
		forms = append(forms, hex.EncodeToString(b), strings.ToUpper(hex.EncodeToString(b)))
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range forms {
		if len(f) >= MinLength {
			r.needle[f] = struct{}{}
		}
	}
	// Longest first: strings.Replacer tries old strings in argument order, so a shorter value that is
	// a prefix of a longer one must not win.
	ordered := make([]string, 0, len(r.needle))
	for n := range r.needle {
		ordered = append(ordered, n)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})
	args := make([]string, 0, 2*len(ordered))
	for _, n := range ordered {
		args = append(args, n, "***")
	}
	r.repl = strings.NewReplacer(args...)
}

// Redact scrubs s.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	repl := r.repl
	r.mu.RUnlock()
	if repl == nil {
		return s
	}
	return repl.Replace(s)
}

// Writer wraps w so everything written is redacted. It works line by line, so a secret is not
// missed because a write happened to split it; call Flush when done to emit a final line that
// has no newline.
func (r *Redactor) Writer(w io.Writer) *RedactingWriter { return &RedactingWriter{r: r, w: w} }

// RedactingWriter is the io.Writer returned by Redactor.Writer.
type RedactingWriter struct {
	r   *Redactor
	w   io.Writer
	buf []byte
}

// Write buffers p and passes every complete line through, redacted.
func (rw *RedactingWriter) Write(p []byte) (int, error) {
	rw.buf = append(rw.buf, p...)
	for {
		i := bytes.IndexByte(rw.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := rw.buf[:i+1]
		if _, err := io.WriteString(rw.w, rw.r.Redact(string(line))); err != nil {
			return 0, err
		}
		rw.buf = rw.buf[i+1:]
	}
}

// Flush writes out any buffered partial line, redacted.
func (rw *RedactingWriter) Flush() error {
	if len(rw.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(rw.w, rw.r.Redact(string(rw.buf)))
	rw.buf = nil
	return err
}
