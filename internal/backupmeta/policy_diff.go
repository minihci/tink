package backupmeta

import (
	"fmt"
	"strings"
)

// policyChangePrefix starts every line that describes a change to a volume's copy policy, so a reader of `plan` can find them among the
// config changes, and so the key itself (which is one escaped line of JSON) never has to be read.
const policyChangePrefix = "backup policy: "

// DescribePolicyChange says in words how a volume's copy policy goes from what it carries (have; "" for none) to what is wanted (want; ""
// for none), one line per difference. The stored policy is one escaped line of JSON, which is the right thing to store and the wrong
// thing to review: a plan shows these lines in its place.
//
// A policy the volume carries that cannot be read (another protocol, edited by hand) is said to be replaced, and everything wanted is then
// listed as added, since there is nothing to compare it with.
func DescribePolicyChange(have, want string) []string {
	if have == want {
		return nil
	}
	if want == "" {
		return []string{policyChangePrefix + describeRemoval(have)}
	}
	w, err := ParsePolicy(want)
	if err != nil { // BuildPolicy made it, so this is a bug; show it rather than say nothing
		return []string{fmt.Sprintf("%sset to %s", policyChangePrefix, want)}
	}
	if have == "" {
		return prefixed(describeAll(w, "+"))
	}
	h, err := ParsePolicy(have)
	if err != nil {
		lines := []string{fmt.Sprintf("replaced (the live one cannot be read: %v), with:", err)}
		return prefixed(append(lines, describeAll(w, "+")...))
	}
	return prefixed(diffPolicies(h, w))
}

func prefixed(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = policyChangePrefix + l
	}
	return out
}

// describeRemoval says what removing a policy stops. have may not be readable, in which case nothing more is known.
func describeRemoval(have string) string {
	h, err := ParsePolicy(have)
	if err != nil {
		return "removed (the declaration no longer has copies or verify)"
	}
	var stops []string
	if len(h.Copies) > 0 {
		stops = append(stops, "copied")
	}
	if h.Verify != nil {
		stops = append(stops, "verified")
	}
	out := "removed (the declaration no longer has copies or verify)"
	if len(stops) > 0 {
		out += ": the volume stops being " + strings.Join(stops, " and ")
	}
	return out
}

func describeAll(p BackupPolicy, mark string) []string {
	var out []string
	for _, c := range p.Copies {
		out = append(out, mark+" "+describeCopy(c))
	}
	if p.Verify != nil {
		out = append(out, mark+" "+describeVerify(*p.Verify))
	}
	return out
}

func diffPolicies(h, w BackupPolicy) []string {
	var out []string
	if h.Proto != w.Proto {
		out = append(out, fmt.Sprintf("~ policy protocol %d -> %d", h.Proto, w.Proto))
	}
	had := map[string]PolicyCopy{}
	for _, c := range h.Copies {
		had[c.Target.Name] = c
	}
	wanted := map[string]bool{}
	for _, c := range w.Copies {
		wanted[c.Target.Name] = true
		old, ok := had[c.Target.Name]
		switch {
		case !ok:
			out = append(out, "+ "+describeCopy(c))
		case old != c:
			out = append(out, "~ "+describeCopyChange(old, c))
		}
	}
	for _, c := range h.Copies {
		if !wanted[c.Target.Name] {
			out = append(out, "- "+describeCopy(c))
		}
	}
	switch {
	case h.Verify == nil && w.Verify != nil:
		out = append(out, "+ "+describeVerify(*w.Verify))
	case h.Verify != nil && w.Verify == nil:
		out = append(out, "- "+describeVerify(*h.Verify))
	case h.Verify != nil && w.Verify != nil && !sameVerify(*h.Verify, *w.Verify):
		out = append(out, fmt.Sprintf("~ %s -> %s", describeVerify(*h.Verify), describeVerify(*w.Verify)))
	}
	return out
}

func describeCopy(c PolicyCopy) string {
	return fmt.Sprintf("copy to %s (%s): schedule %q, keep %s", c.Target.Name, describeWhere(c.Target), c.Schedule, c.Retain)
}

// describeWhere is where a target is, the way a person would say it.
func describeWhere(t PolicyTarget) string {
	switch {
	case t.Remote != "" && t.Pool != "":
		return fmt.Sprintf("remote %s, pool %s", t.Remote, t.Pool)
	case t.Remote != "":
		return "remote " + t.Remote
	default:
		return "pool " + t.Pool
	}
}

// describeCopyChange lists only what differs about a copy that is in both.
func describeCopyChange(old, now PolicyCopy) string {
	var parts []string
	if old.Schedule != now.Schedule {
		parts = append(parts, fmt.Sprintf("schedule %q -> %q", old.Schedule, now.Schedule))
	}
	if old.Retain != now.Retain {
		parts = append(parts, fmt.Sprintf("keep %s -> %s", old.Retain, now.Retain))
	}
	if old.Target != now.Target {
		parts = append(parts, fmt.Sprintf("now at %s (was %s)", describeWhere(now.Target), describeWhere(old.Target)))
	}
	return fmt.Sprintf("copy to %s: %s", now.Target.Name, strings.Join(parts, ", "))
}

func describeVerify(v PolicyVerify) string {
	every := v.Every
	if every == "" {
		every = "(no cadence)"
	}
	s := "verify " + every
	if c := v.Check; c != nil {
		s += fmt.Sprintf(", check in %s: %s", c.Image, strings.Join(c.Command, " "))
		if c.Mount != "" {
			s += " (volume mounted at " + c.Mount + ")"
		}
	}
	return s
}

func sameVerify(a, b PolicyVerify) bool {
	if a.Every != b.Every || (a.Check == nil) != (b.Check == nil) {
		return false
	}
	return a.Check == nil || (a.Check.Image == b.Check.Image && a.Check.Mount == b.Check.Mount && strings.Join(a.Check.Command, "\x00") == strings.Join(b.Check.Command, "\x00"))
}
