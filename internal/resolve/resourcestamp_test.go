package resolve

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/ownership"
)

// The resource stamp (issue #309): what an object was made for. Every kind that carries the owner stamp carries it; an ordinary apply keeps it current
// on an object this stack owns; adopting claims a copy; and a plan says so in lines and never in a change.

// configOf is the config (an image: the properties) the fake holds for the one object it was seeded with.
func (f *ownFake) configOf(kind Kind) map[string]string {
	switch kind {
	case KindProject:
		return f.projects["p"].Config
	case KindProfile:
		return f.profiles["p"].Config
	case KindStorageVolume:
		return f.volumes["default/p"].Config
	case KindInstance:
		return f.instances["p"].Config
	case KindNetwork:
		return f.networks["p"].Config
	case KindNetworkACL:
		return f.acls["p"].Config
	case KindImage:
		return f.images["fp1"].Properties
	}
	return nil
}

func (f *ownFake) writes() int {
	return len(f.updated) + len(f.patches) + len(f.created)
}

func allKinds() []Kind {
	return []Kind{KindProject, KindNetwork, KindNetworkACL, KindProfile, KindStorageVolume, KindInstance, KindFile, KindIncus, KindImage, KindExec,
		KindBackupTarget, KindStack, KindHelper, KindPublish}
}

func TestTheAddressOfEachDeclaredKind(t *testing.T) {
	for _, c := range []struct {
		r    Resource
		want string
	}{
		{Resource{Kind: KindInstance, Name: "app", Project: "ha"}, "instance/ha/app"},
		{Resource{Kind: KindInstance, Name: "app"}, "instance/default/app"},
		{Resource{Kind: KindProfile, Name: "base", Project: "ha"}, "profile/ha/base"},
		{Resource{Kind: KindProfile, Name: "base", Project: "default"}, "profile/default/base"},
		{Resource{Kind: KindStorageVolume, Name: "data", Project: "ha", Pool: "fast"}, "storage-volume/ha/fast/data"},
		{Resource{Kind: KindStorageVolume, Name: "data", Project: "ha"}, "storage-volume/ha/default/data"},
		{Resource{Kind: KindStorageVolume, Name: "data"}, "storage-volume/default/default/data"},
		// an image is found by its alias, in the project the stack declares: that is its address, not the document's name
		{Resource{Kind: KindImage, Name: "the-image", Alias: "haos", Project: "ha"}, "image/ha/haos"},
		{Resource{Kind: KindImage, Name: "the-image", Alias: "haos"}, "image/default/haos"},
		{Resource{Kind: KindProject, Name: "ha"}, "project/ha"},
		{Resource{Kind: KindNetwork, Name: "br-lab"}, "network/br-lab"},
		{Resource{Kind: KindNetworkACL, Name: "lan"}, "network-acl/lan"},
		{Resource{Kind: KindFile, Name: "f"}, ""},
	} {
		if got := addressOf(c.r); got != c.want {
			t.Errorf("addressOf(%s %q) = %q, want %q", c.r.Kind, c.r.Name, got, c.want)
		}
	}
}

// The leaf package spells the kinds itself (it imports nothing of tink's): hold its words to the stack's, and the list of stamped kinds to what the
// converger says carries a stamp, so a kind added to one cannot be left out of the other.
func TestTheStampedKindsAreTheOnesWithAnAddress(t *testing.T) {
	var carrying []Kind
	for _, k := range allKinds() {
		addr := ownership.Address(string(k), "p", "pool", "n")
		if stampedKind(k) != (addr != "") {
			t.Errorf("kind %s: stampedKind = %v but the address %q", k, stampedKind(k), addr)
		}
		if stampedKind(k) {
			carrying = append(carrying, k)
		}
	}
	if !reflect.DeepEqual(sortedKinds(carrying), sortedKinds(stampedKinds)) {
		t.Errorf("the tests run %v, the converger stamps %v: a stamped kind is missing from stampedKinds", stampedKinds, carrying)
	}
}

func sortedKinds(ks []Kind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	sort.Strings(out)
	return out
}

// An ordinary apply keeps the stamps current on an object this stack already owns: the missing resource stamp is written, narrowly, and it is not a
// change, so the object still reads "no changes" and nothing is restarted, whatever its restart: says.
func TestAnOrdinaryApplyWritesAMissingResourceStampAndRestartsNothing(t *testing.T) {
	for _, kind := range stampedKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newOwnFake()
			r := f.seed(kind, unbound)
			r.Restart = kind == KindInstance // the stamp is not worth a restart, and says so by not reading this
			out, err := applyOne(f, r, ownOpts("lab"), func(string, ...any) {})
			if err != nil || out != outConverged {
				t.Fatalf("outcome %v, %v", out, err)
			}
			got := f.configOf(kind)
			if got[ownership.ResourceKey] != addressOf(r) || got[ownership.Key] != "lab" {
				t.Errorf("the object carries %v, want the owner stamp kept and the resource stamp written", got)
			}
			bag := stampedResource(kind).Config
			if kind == KindImage {
				bag = stampedResource(kind).Properties
			}
			for k, v := range bag {
				if got[k] != v {
					t.Errorf("%s was changed by the stamp: %q", k, got[k])
				}
			}
			want := len(bag) + 2
			if rec, _, ok := appliedRecordFor(r, "lab"); ok {
				want++ // the applied record is written with it (#312), and is the one this declaration applies
				if got[ownership.AppliedKey] != rec {
					t.Errorf("the applied record is %q, want %q", got[ownership.AppliedKey], rec)
				}
			}
			if len(got) != want {
				t.Errorf("more than the stamps was written: %v", got)
			}
			if len(f.stateChanges) != 0 {
				t.Errorf("a stamp restarted something: %v", f.stateChanges)
			}
			if kind == KindInstance && (len(f.updated) != 0 || len(f.patches) != 1) {
				t.Errorf("an instance's stamp is a PATCH of the key, not a whole-instance write (which converging follows with a restart): updated %v patches %v", f.updated, f.patches)
			}
			// and once written, the plan reads it as here and a second apply writes nothing
			before := f.writes()
			if p, err := planOne(f, r, ownOpts("lab")); err != nil || p.Ownership.State != ownership.Here || p.Action != ActionNone {
				t.Errorf("after the write the plan reads %v %v (%v)", p.Ownership.State, p.Action, err)
			}
			if _, err := applyOne(f, r, ownOpts("lab"), func(string, ...any) {}); err != nil || f.writes() != before {
				t.Errorf("a second apply wrote again: %v %d writes", err, f.writes()-before)
			}
		})
	}
}

// With a real change to make, the stamp goes in the same write.
func TestAnOrdinaryApplyPutsAMissingResourceStampInTheWriteOfARealChange(t *testing.T) {
	for _, kind := range []Kind{KindProfile, KindStorageVolume, KindInstance, KindNetwork, KindNetworkACL} {
		t.Run(string(kind), func(t *testing.T) {
			f := newOwnFake()
			r := f.seed(kind, unbound)
			r.Config = map[string]string{"user.note": "new"} // the object carries "x" (nothing, for an ACL)
			if kind == KindNetwork {
				r.AllowDisruption = true
			}
			out, err := applyOne(f, r, ownOpts("lab"), func(string, ...any) {})
			if err != nil || out != outChanged {
				t.Fatalf("outcome %v, %v", out, err)
			}
			got := f.configOf(kind)
			if got["user.note"] != "new" || got[ownership.ResourceKey] != addressOf(r) || got[ownership.Key] != "lab" {
				t.Errorf("the object carries %v, want the change, the owner stamp and the resource stamp", got)
			}
			if rec, _, _ := appliedRecordFor(r, "lab"); got[ownership.AppliedKey] != rec || rec == "" {
				t.Errorf("the applied record is %q, want %q, in the same write", got[ownership.AppliedKey], rec)
			}
			if len(f.patches) != 0 {
				t.Errorf("the stamp was written by a call of its own beside the update: %v", f.patches)
			}
			if len(f.updated) != 1 {
				t.Errorf("want the one write that carries both, got %v", f.updated)
			}
			if len(f.stateChanges) != 0 {
				t.Errorf("an update without restart: true restarted %v", f.stateChanges)
			}
		})
	}
}

// An ordinary apply claims nothing: a copy, and an object another stack stamped, are written by nobody but adopting.
func TestAnOrdinaryApplyLeavesCopiesAndOtherStacksObjectsAlone(t *testing.T) {
	for _, state := range []string{stampedCopy, stampedElsewh, unstamped, stampedHere} {
		for _, kind := range stampedKinds {
			t.Run(state+"/"+string(kind), func(t *testing.T) {
				f := newOwnFake()
				r := f.seed(kind, state)
				before := map[string]string{}
				for k, v := range f.configOf(kind) {
					before[k] = v
				}
				if _, err := applyOne(f, r, ownOpts("lab"), func(string, ...any) {}); err != nil {
					t.Fatal(err)
				}
				if f.writes() != 0 || !reflect.DeepEqual(f.configOf(kind), before) {
					t.Errorf("an object that is %s was written: %v -> %v (%d writes)", state, before, f.configOf(kind), f.writes())
				}
			})
		}
	}
}

// The stamp dimension is never an action: whatever the stamps say, the plan of an object that otherwise matches is "no changes" and carries the state.
func TestAPlanReadsACopyAndAnUnboundObjectAsStateAndNeverAsAChange(t *testing.T) {
	for _, state := range []string{stampedCopy, unbound} {
		for _, kind := range stampedKinds {
			f := newOwnFake()
			r := f.seed(kind, state)
			p, err := planOne(f, r, ownOpts("lab"))
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]ownership.State{stampedCopy: ownership.Copy, unbound: ownership.Unbound}[state]
			if p.Action != ActionNone || len(p.Changes) != 0 || len(p.Blocked()) != 0 || p.Ownership.State != want {
				t.Errorf("%s/%s: action %v changes %q blocked %v state %v, want no changes and %v", kind, state, p.Action, p.Changes, details(p.Blocked()), p.Ownership.State, want)
			}
		}
	}
}

// Adopting claims what carries no stamp and what is a copy, with both stamps in one narrow write, and leaves another stack's object alone.
func TestAdoptClaimsUnstampedObjectsAndCopiesAndLeavesAnotherStacksAlone(t *testing.T) {
	for _, kind := range stampedKinds {
		for _, state := range []string{unstamped, stampedCopy} {
			t.Run(string(kind)+"/"+state, func(t *testing.T) {
				f := newOwnFake()
				r := f.seed(kind, state)
				res, err := Adopt(t.Context(), f, adoptLevels(r), "lab", 0)
				if err != nil {
					t.Fatal(err)
				}
				got := f.configOf(kind)
				if len(res.Stamped) != 1 || got[ownership.Key] != "lab" || got[ownership.ResourceKey] != addressOf(r) {
					t.Fatalf("stamped %v, the object carries %v, want it claimed with both stamps", res.Stamped, got)
				}
				if wantCopies := map[string]int{unstamped: 0, stampedCopy: 1}[state]; res.Copies != wantCopies {
					t.Errorf("copies counted = %d, want %d", res.Copies, wantCopies)
				}
				if len(f.updated)+len(f.patches) != 1 {
					t.Errorf("claiming is one write: updated %v patches %v", f.updated, f.patches)
				}
				if len(f.stateChanges) != 0 {
					t.Errorf("adopting changed an instance's state: %v", f.stateChanges)
				}
				p, _ := planOne(f, r, ownOpts("lab"))
				if p.Ownership.State != ownership.Here {
					t.Errorf("after adopting the object reads %v, want here", p.Ownership.State)
				}
			})
		}
		t.Run(string(kind)+"/another stack's", func(t *testing.T) {
			f := newOwnFake()
			r := f.seed(kind, stampedElsewh)
			before := map[string]string{}
			for k, v := range f.configOf(kind) {
				before[k] = v
			}
			res, err := Adopt(t.Context(), f, adoptLevels(r), "lab", 0)
			if err != nil || len(res.Stamped) != 0 || f.writes() != 0 || !reflect.DeepEqual(f.configOf(kind), before) {
				t.Errorf("another stack's object was claimed: %v %v, %v -> %v", res.Stamped, err, before, f.configOf(kind))
			}
		})
	}
}

// An unbound object is this stack's already: adopting leaves it to the apply that follows, which writes the one stamp it lacks.
func TestAdoptLeavesAnUnboundObjectToTheOrdinaryApply(t *testing.T) {
	f := newOwnFake()
	r := f.seed(KindProfile, unbound)
	res, err := Adopt(t.Context(), f, adoptLevels(r), "lab", 0)
	if err != nil || len(res.Stamped) != 0 || f.writes() != 0 {
		t.Errorf("adopt wrote an object this stack owns already: %v %v %d", res.Stamped, err, f.writes())
	}
	if _, err := applyOne(f, r, ownOpts("lab"), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if f.profiles["p"].Config[ownership.ResourceKey] != "profile/default/p" {
		t.Errorf("the apply did not write the resource stamp: %v", f.profiles["p"].Config)
	}
}

func TestTheAdoptSummaryCountsTheCopiesItClaimed(t *testing.T) {
	a := AdoptResult{Stamped: make([]string, 3), ByKind: map[Kind]int{KindStorageVolume: 3}, Copies: 2}
	lines := strings.Join(a.Lines(), "\n")
	if !strings.Contains(lines, "2 of them carried stamps made for another resource (copies)") {
		t.Errorf("the summary does not say it claimed copies: %s", lines)
	}
}

// What the report says, and what it leaves out.

func TestTheReportGroupsCopiesAndUnboundObjectsAndLeavesRestorePointsOut(t *testing.T) {
	rep := NewOwnershipReport("lab", false)
	add := func(kind Kind, name string, st ownership.State, restore bool) {
		rep.Add(PlannedResource{Resource: Resource{Kind: kind, Name: name}, Ownership: StampState{State: st, RestorePoint: restore}})
	}
	add(KindInstance, "web-copy", ownership.Copy, false)
	add(KindStorageVolume, "data-copy", ownership.Copy, false)
	add(KindStorageVolume, "restore-point", ownership.Copy, true)
	add(KindProfile, "old", ownership.Unbound, false)
	add(KindProfile, "fine", ownership.Here, false)
	lines := rep.Lines()
	if len(lines) != 2 {
		t.Fatalf("want one line for the copies and one for the unbound objects, got %q", lines)
	}
	if !strings.Contains(lines[0], "2 declared objects carry stamps made for another resource (copies?): instance/web-copy, storage-volume/data-copy;") {
		t.Errorf("copies line = %q", lines[0])
	}
	if strings.Contains(lines[0], "restore-point") || strings.Contains(strings.Join(lines, "\n"), "fine") {
		t.Errorf("a restore point or an object that is fine is listed: %q", lines)
	}
	if !strings.Contains(lines[1], "1 declared object has no resource stamp yet: profile/old;") || !strings.Contains(lines[1], "`tink plan apply` writes it") {
		t.Errorf("unbound line = %q", lines[1])
	}
}

// A restore point of a declared volume carries its source's stamps, so it is not the same object; it is not a stray copy either, by its markers.
func TestARestorePointIsACopyThatIsNotCounted(t *testing.T) {
	for _, marker := range []string{backupmeta.MarkerCopyOf, backupmeta.MarkerPartialOf} {
		f := newOwnFake()
		r := f.seed(KindStorageVolume, stampedCopy)
		f.volumes["default/p"].Config[marker] = "default/default/other"
		p, err := planOne(f, r, ownOpts("lab"))
		if err != nil {
			t.Fatal(err)
		}
		if p.Ownership.State != ownership.Copy || !p.Ownership.RestorePoint {
			t.Errorf("%s: a restore point reads %v restore point %v, want a copy that is marked as a restore point", marker, p.Ownership.State, p.Ownership.RestorePoint)
		}
	}
	f := newOwnFake()
	r := f.seed(KindStorageVolume, stampedCopy)
	if p, _ := planOne(f, r, ownOpts("lab")); p.Ownership.RestorePoint {
		t.Error("a volume with no marker is not a restore point")
	}
}

// What tink's own copies of a volume keep: the owner stamp and the resource stamp, so a restore point or a restored volume reads as a copy.
func TestACopyOfAVolumeKeepsBothStampsAndDropsWhatHappenedToIt(t *testing.T) {
	for _, k := range []string{ownership.Key, ownership.ResourceKey} {
		if ownership.DroppedOnCopy(k, backupmeta.PolicyKey) {
			t.Errorf("%s is scrubbed from a copy, which would make the copy read as the original", k)
		}
	}
	if !ownership.DroppedOnCopy(ownership.AppliedKey, backupmeta.PolicyKey) {
		t.Error("the applied record describes the original, not the copy")
	}
}
