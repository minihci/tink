// Package finding is the one shape tink uses to say "something was noticed about this": by the join, by the Headscale projection, by a plan
// (what blocks a resource, what could not be verified, a fact worth reading), and by whatever reports on the daemon later.
//
//	Severity  error | warning | note
//	Reason    a fixed phrase that names the kind of problem, safe to leave the site
//	Subject   the resource it is about, in tink's form ("publish/whoami"); empty when it is about no one resource
//	Detail    text for a person; it may name things
//
// Error means the finding blocks the thing it is about. That is the meaning every use of it has: it is the effect on the work, not a measure of
// urgency. Urgency is a separate matter, for whatever sends a notification.
//
// A Reason is required. It is a phrase written once at the site that raises the finding ("image drift", "a stamp cannot be read"), never built from
// what was found, so that it can go into a status document, a log key or a notification without a boundary check, and so that a test can key on it.
// New refuses an empty one; Errorf, Warnf and Notef are New for a reason that is a literal at the site, and panic on a programming mistake instead of
// returning an error nobody could act on.
//
// This is a leaf package: it imports nothing of tink's, so any package may use it.
package finding

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Severity says what a finding does to the work it is about. The zero value is the weakest, so a finding that forgot its severity does not stop anything.
type Severity int

const (
	// Note is a fact worth reading that is not a problem.
	Note Severity = iota
	// Warning is a problem that does not stop the work.
	Warning
	// Error stops the work it is about.
	Error
)

func (s Severity) String() string {
	switch s {
	case Note:
		return "note"
	case Warning:
		return "warning"
	case Error:
		return "error"
	}
	return fmt.Sprintf("severity(%d)", int(s))
}

// A Finding is something that was noticed. Build one with New, Errorf, Warnf or Notef; the zero Finding has no reason and is not valid.
type Finding struct {
	Severity Severity
	Reason   string
	Subject  string
	Detail   string
}

// ErrNoReason is what New returns for a finding with no reason.
var ErrNoReason = errors.New("a finding needs a reason")

// New makes a finding. It refuses an empty (or blank) reason and a severity that is not one of the three.
func New(sev Severity, reason, subject, detail string) (Finding, error) {
	if strings.TrimSpace(reason) == "" {
		return Finding{}, ErrNoReason
	}
	if sev < Note || sev > Error {
		return Finding{}, fmt.Errorf("a finding needs a severity of error, warning or note, not %v", sev)
	}
	return Finding{Severity: sev, Reason: reason, Subject: subject, Detail: detail}, nil
}

func must(sev Severity, reason, subject, detail string) Finding {
	f, err := New(sev, reason, subject, detail)
	if err != nil {
		// A reason is a literal at the site that raises the finding, so this is a mistake in the code, caught by the first test that reaches the site.
		panic("finding: " + err.Error() + " (detail: " + detail + ")")
	}
	return f
}

// Errorf is New for an error whose reason is a literal at the site; the detail is formatted. It panics on an empty reason.
func Errorf(reason, subject, format string, args ...any) Finding {
	return must(Error, reason, subject, fmt.Sprintf(format, args...))
}

// Warnf is Errorf for a warning.
func Warnf(reason, subject, format string, args ...any) Finding {
	return must(Warning, reason, subject, fmt.Sprintf(format, args...))
}

// Notef is Errorf for a note.
func Notef(reason, subject, format string, args ...any) Finding {
	return must(Note, reason, subject, fmt.Sprintf(format, args...))
}

// String is the finding as one line for a person: the severity, the subject when there is one, and the detail. The reason is not in it: it is the
// key to group and count by, and the detail is what says it in words.
func (f Finding) String() string {
	if f.Subject == "" {
		return fmt.Sprintf("%s: %s", f.Severity, f.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", f.Severity, f.Subject, f.Detail)
}

// Count is how many of fs have severity sev.
func Count(fs []Finding, sev Severity) int {
	n := 0
	for _, f := range fs {
		if f.Severity == sev {
			n++
		}
	}
	return n
}

// HasErrors reports whether any of fs stops the thing it is about.
func HasErrors(fs []Finding) bool { return Count(fs, Error) > 0 }

// Sort orders fs for a report that reads the same for the same input: errors first, then warnings, then notes, then by subject, then by detail. It
// is stable, so findings equal in all of those keep the order they were raised in.
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Detail < b.Detail
	})
}
