package resolve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/ownership"
)

// Adopting is the one pass that stamps objects that exist already (ownership.go; #133, "First-apply churn"). It is a pass of its own and not a part of
// converging, for three reasons that the design lists as hazards:
//
//   - It must not restart anything. Converging an instance restarts it after any in-place update when `restart: true`, and the stamp is not worth that.
//     So adopting does not go through the update path at all: it reads the object, and writes ONE key with the narrowest write the server offers (an
//     instance: a PATCH of that key; the other kinds: a read and a write of the whole object with the key added, under the etag it was read with). It
//     never reads Resource.Restart and calls nothing that changes an instance's state.
//   - It must not be noise. The stamp is outside the diff of every kind, so adopting is never an "update" and a plan before it and after it read the same.
//   - It must not guess. It stamps a declared object that exists, is unstamped, and is one tink is willing to change. An object stamped by another stack is
//     left alone (taking it over is phase 2, `--adopt-from`), and so is anything that is not there (apply creates it, stamped).
//
// The kinds that are create-only elsewhere (a project, an image) get their stamp the same way, by a narrow update that changes that key and nothing
// else; an image's write echoes its `profiles`, because Incus resets them to ["default"] when a write leaves them out.

// AdoptResult is what an adopt pass did.
type AdoptResult struct {
	// Stamped names what was stamped, in the order the stack's levels have them.
	Stamped []string
	// ByKind counts Stamped by kind.
	ByKind map[Kind]int
	// RunningVMs is how many of the stamped instances are virtual machines that were running. Incus allows the write live but marks such a machine as
	// needing a reset, so its next reboot is a full restart and not a warm one. Said, not avoided: it cannot be.
	RunningVMs int
	// Skipped says why a declared object that exists was not stamped, "kind/name: reason". Objects that are already stamped (here or elsewhere) and
	// objects that do not exist yet are not skipped, they are simply not adopted, and are not listed.
	Skipped []string
}

// Lines is what `plan apply --adopt` prints: the summary, then one line per object left alone and why.
//
// "restarted 0" is true by construction and is tested as such: the pass has no call that stops, starts, restarts or freezes an instance (the fakes of
// adopt_test.go fail on any state change). It is the count of restarts made BY ADOPTING, not of the apply it runs beside.
func (a AdoptResult) Lines() []string {
	var names []string
	for k := range a.ByKind {
		names = append(names, string(k))
	}
	sort.Strings(names)
	var kinds []string
	for _, k := range names {
		kinds = append(kinds, fmt.Sprintf("%d %s", a.ByKind[Kind(k)], k))
	}
	var out []string
	switch len(a.Stamped) {
	case 0:
		out = append(out, fmt.Sprintf("adopt: nothing to stamp (no declared object is without a %s); restarted 0", ownership.Key))
	default:
		noun := "objects"
		if len(a.Stamped) == 1 {
			noun = "object"
		}
		out = append(out, fmt.Sprintf("adopt: stamped %s on %d %s (%s); restarted 0", ownership.Key, len(a.Stamped), noun, strings.Join(kinds, ", ")))
	}
	if a.RunningVMs > 0 {
		out = append(out, fmt.Sprintf("adopt: %d running virtual machine(s) were stamped live; Incus marks such a machine as needing a reset, so its next reboot is a full restart", a.RunningVMs))
	}
	for _, s := range a.Skipped {
		out = append(out, "adopt: skipped "+s)
	}
	return out
}

// Adopt stamps, with stack's name, every declared object in levels that exists and has no stamp. levels is the stack as Levels built it (each resource
// holds the project it is placed in). A stack with no name is an error: nothing could be written.
//
// It stops at the first failure and returns what had been done up to it, so the caller can report it; nothing is half-written, because each object takes one
// write. opTimeout, when set, replaces the deadline of each call (opbounds.go).
func Adopt(ctx context.Context, server incus.InstanceServer, levels [][]Resource, stack string, opTimeout time.Duration) (AdoptResult, error) {
	res := AdoptResult{ByKind: map[Kind]int{}}
	if stack == "" {
		return res, errors.New("nothing can be adopted by a stack with no name: add a `kind: stack` document to name it")
	}
	limit := opLimit(opTimeout, false)
	for _, level := range levels {
		for _, r := range level {
			if !stampedKind(r.Kind) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return res, fmt.Errorf("adopt interrupted: %w", err)
			}
			label := stampLabel(r)
			out, err := incusapi.Bounded(limit, "adopting "+label, func() (adoptOutcome, error) { return adoptOne(server, r, stack) })
			if err != nil {
				return res, fmt.Errorf("%s: %w", label, err)
			}
			switch {
			case out.skipped != "":
				res.Skipped = append(res.Skipped, label+": "+out.skipped)
			case out.stamped:
				res.Stamped = append(res.Stamped, label)
				res.ByKind[r.Kind]++
				if out.runningVM {
					res.RunningVMs++
				}
			}
		}
	}
	return res, nil
}

// adoptOutcome is what adoptOne found and did for one object.
type adoptOutcome struct {
	stamped   bool
	runningVM bool
	skipped   string // set when the object is one tink will not stamp, and why
}

// adoptOne reads one declared object and, if it exists and has no stamp, writes the stamp. Every kind reads its object with the lookup that tells "absent"
// from "could not ask" (incusapi.Lookup*), so a server that does not answer is an error and never "nothing to do".
func adoptOne(server incus.InstanceServer, r Resource, stack string) (adoptOutcome, error) {
	s := scopedServer(server, r)
	unstamped := func(config map[string]string) bool {
		state, _ := ownership.Classify(config, stack)
		return state == ownership.Unstamped
	}
	stamp := map[string]string{ownership.Key: stack}
	switch r.Kind {
	case KindInstance:
		cur, _, found, err := incusapi.LookupInstance(s, r.Name)
		if err != nil || !found || !unstamped(cur.Config) {
			return adoptOutcome{}, err
		}
		// A PATCH of the one key, on the unscoped connection with the project in the request: not the read-and-write of the whole instance that
		// converging does, and nothing that touches the instance's state.
		if err := incusapi.PatchInstanceConfig(server, r.Project, r.Name, stamp); err != nil {
			return adoptOutcome{}, err
		}
		return adoptOutcome{stamped: true, runningVM: cur.Type == string(api.InstanceTypeVM) && cur.StatusCode == api.Running}, nil
	case KindProfile:
		cur, etag, found, err := incusapi.LookupProfile(s, r.Name)
		if err != nil || !found || !unstamped(cur.Config) {
			return adoptOutcome{}, err
		}
		put := cur.Writable()
		put.Config = withStamp(put.Config, stack)
		return adoptOutcome{stamped: true}, s.UpdateProfile(r.Name, put, etag)
	case KindStorageVolume:
		pool := r.Pool
		if pool == "" {
			pool = "default"
		}
		cur, _, found, err := incusapi.LookupVolume(s, pool, "custom", r.Name)
		if err != nil || !found || !unstamped(cur.Config) {
			return adoptOutcome{}, err
		}
		return adoptOutcome{stamped: true}, incusapi.MergeVolumeConfig(server, r.Project, pool, r.Name, stamp)
	case KindProject:
		cur, etag, found, err := incusapi.LookupProject(s, r.Name)
		if err != nil || !found || !unstamped(cur.Config) {
			return adoptOutcome{}, err
		}
		put := cur.Writable()
		put.Config = withStamp(put.Config, stack)
		return adoptOutcome{stamped: true}, s.UpdateProject(r.Name, put, etag)
	case KindNetwork:
		cur, etag, found, err := incusapi.LookupNetwork(s, r.Name)
		if err != nil || !found {
			return adoptOutcome{}, err
		}
		// the same refusals as planNetwork: tink never changes a network Incus does not manage, or one that is not a finished bridge
		switch {
		case !cur.Managed:
			return adoptOutcome{skipped: "Incus does not manage it, and tink never changes such a network"}, nil
		case cur.Type != "bridge":
			return adoptOutcome{skipped: fmt.Sprintf("it is a managed network of type %q; tink manages bridges only", cur.Type)}, nil
		case cur.Status != "" && cur.Status != "Created":
			return adoptOutcome{skipped: fmt.Sprintf("its status is %q, not Created", cur.Status)}, nil
		case !unstamped(cur.Config):
			return adoptOutcome{}, nil
		}
		put := cur.Writable()
		put.Config = withStamp(put.Config, stack)
		return adoptOutcome{stamped: true}, s.UpdateNetwork(r.Name, put, etag)
	case KindNetworkACL:
		cur, etag, found, err := incusapi.LookupNetworkACL(s, r.Name)
		if err != nil || !found || !unstamped(cur.Config) {
			return adoptOutcome{}, err
		}
		put := cur.Writable()
		put.Config = withStamp(put.Config, stack)
		return adoptOutcome{stamped: true}, s.UpdateNetworkACL(r.Name, put, etag)
	case KindImage:
		alias, _, found, err := incusapi.LookupImageAlias(s, r.Alias)
		if err != nil || !found {
			return adoptOutcome{}, err
		}
		cur, etag, found, err := incusapi.LookupImage(s, alias.Target)
		if err != nil || !found || !unstamped(cur.Properties) {
			return adoptOutcome{}, err
		}
		// Writable() is the whole of what an image PUT carries, profiles included: leaving them out would reset them to ["default"]
		put := cur.Writable()
		put.Properties = withStamp(put.Properties, stack)
		return adoptOutcome{stamped: true}, s.UpdateImage(cur.Fingerprint, put, etag)
	}
	return adoptOutcome{}, nil
}

// withStamp is config with the stamp added, as a new map.
func withStamp(config map[string]string, stack string) map[string]string {
	return ownership.With(config, stack)
}
