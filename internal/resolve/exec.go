package resolve

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// runIncus (plan.go/apply.go) shells out to the incus binary on the host
// resolve itself runs on; kind: exec instead runs inside the guest
// instance named by Instance, over incusapi.ExecInGuest -- the same
// WebSocket exec protocol `incus exec` itself is built on (see that
// function's own doc comment for why it lives in incusapi rather than
// here: internal/ingress's Caddy-reload apply() needed the exact same
// primitive for the exact same reason). A USB device finishing a driver
// bind or a Caddy process reloading its config both live on the guest
// side, which is the entire reason kind: exec exists next to kind: incus
// rather than one field broadened to cover both.

// triggerHash is what gets stored in and compared against the
// user.tink.exec.<name>.trigger-hash config key -- see Resource.Triggers'
// own doc comment for why this exists at all. \x00-joined rather than
// plain concatenation so, e.g., ["ab", "c"] and ["a", "bc"] hash
// differently.
func triggerHash(triggers []string) string {
	sum := sha256.Sum256([]byte(strings.Join(triggers, "\x00")))
	return hex.EncodeToString(sum[:])
}

func triggerHashConfigKey(name string) string {
	return "user.tink.exec." + name + ".trigger-hash"
}

// execConverged answers planExec's and runExec's shared "was this
// already done" question -- the same question Check already answers for
// kind: incus, with Triggers' stored-hash fallback for when no live
// check can be written at all (see Resource.Triggers' own doc comment
// for exactly when that is, and why Check stays preferred whenever one
// can be written).
//
// The instance-existence check up front matches planProject's,
// planProfile's, planInstance's, planImage's and planFile's own
// established convention here: a failed Get means "doesn't exist yet,"
// answered as ActionCreate/not-converged, never propagated as a plan
// error. Found live, not designed in up front: Plan computes every
// resource's plan concurrently against current reality (see Plan's own
// doc comment -- "nothing here depends on another resource existing
// yet"), so an exec resource targeting an instance that's only *planned*
// to exist, not created yet, hit this for real the first time this ran
// against a from-scratch stack -- Check's ExecInstance and Triggers' own
// GetInstance both fail outright against a project/instance that isn't
// there yet, and without this guard that surfaced as a hard Plan error
// instead of the "would create" every other kind already reports
// correctly in exactly this situation.
func execConverged(server incus.InstanceServer, r Resource) (bool, error) {
	inst, _, err := server.GetInstance(r.Instance)
	if err != nil {
		return false, nil
	}

	if len(r.Check) > 0 {
		code, _, err := incusapi.ExecInGuest(server, r.Instance, r.Check)
		if err != nil {
			return false, err
		}
		return code == 0, nil
	}

	return inst.Config[triggerHashConfigKey(r.Name)] == triggerHash(r.Triggers), nil
}

// planExec reuses ActionCreate for "needs to run," the same convention
// planIncus already established for kind: incus -- not semantically a
// creation, but applyOne's/createOne's dispatch already treats
// ActionCreate as "the thing this plan decided needs doing," and a
// second Action value that means exactly the same thing here would just
// be two names for one concept.
func planExec(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	converged, err := execConverged(server, r)
	if err != nil {
		return PlannedResource{}, err
	}
	if converged {
		return PlannedResource{Resource: r, Action: ActionNone}, nil
	}
	return PlannedResource{Resource: r, Action: ActionCreate, Changes: []string{fmt.Sprintf("would run: %v", r.Command)}}, nil
}

// runExec re-checks convergence rather than trusting the plan it was
// handed -- Plan and Apply aren't atomic with each other (see Apply's
// own doc comment on level-by-level execution), and a Command whose
// whole reason for having a Check/Triggers guard at all is that it's
// unsafe to double-run deserves that guard actually enforced at the
// moment it runs, not assumed still true from a moment earlier.
func runExec(server incus.InstanceServer, r Resource) error {
	s := scopedServer(server, r)

	converged, err := execConverged(s, r)
	if err != nil {
		return err
	}
	if converged {
		return nil
	}

	code, output, err := incusapi.ExecInGuest(s, r.Instance, r.Command)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("running %v on %s: exit %d: %s", r.Command, r.Instance, code, output)
	}

	if len(r.Triggers) == 0 {
		return nil
	}

	inst, etag, err := s.GetInstance(r.Instance)
	if err != nil {
		return fmt.Errorf("reading %s to record %s's trigger hash: %w", r.Instance, r.Name, err)
	}
	put := inst.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	put.Config[triggerHashConfigKey(r.Name)] = triggerHash(r.Triggers)
	op, err := s.UpdateInstance(r.Instance, put, etag)
	if err != nil {
		return fmt.Errorf("recording %s's trigger hash on %s: %w", r.Name, r.Instance, err)
	}
	return op.Wait()
}
