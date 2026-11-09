package join

import (
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/observe"
	"github.com/minihci/tink/internal/ownership"
	"github.com/minihci/tink/internal/resolve"
)

// The resource stamp (issue #309), read by the join: an instance made with `incus copy` carries every stamp of the original, so the stamps of an instance
// that is a copy (or that has no resource stamp yet) are not read, and one finding says so.

// stampedLikeApplyDid gives every instance of the fake the two stamps `plan apply` would have written, so each reads as here.
func stampedLikeApplyDid(f *serverFake) {
	for i := range f.instances {
		in := &f.instances[i]
		cfg := map[string]string{}
		for k, v := range in.Config {
			cfg[k] = v
		}
		cfg[ownership.Key] = "lab"
		cfg[ownership.ResourceKey] = ownership.Address(ownership.KindInstance, in.Project, "", in.Name)
		in.Config = cfg
	}
}

// copyOf adds an instance as `incus copy` would make it: another name, every user.* key of the original.
func copyOf(f *serverFake, name, copyName string, drop ...string) *api.Instance {
	for _, in := range f.instances {
		if in.Name != name {
			continue
		}
		cfg := map[string]string{}
		for k, v := range in.Config {
			cfg[k] = v
		}
		for _, k := range drop {
			delete(cfg, k)
		}
		c := in
		c.Name, c.Config = copyName, cfg
		f.instances = append(f.instances, c)
		return &f.instances[len(f.instances)-1]
	}
	panic("no instance " + name)
}

func TestACopyOfAPublishedInstanceIsNotASecondPublisherAndIsNotAnError(t *testing.T) {
	f := serverOf(t, load(t))
	stampedLikeApplyDid(f)
	copyOf(f, "whoami", "whoami-copy")

	res := fromServer(t, f)
	if res.Errors() != 0 {
		t.Fatalf("a copy blocked the join: %s", messages(res, finding.Error))
	}
	for name, got := range map[string]string{"dns": res.DNS, "mesh": res.Mesh, "caddy": res.Caddy, "sso": res.SSO, "acl": res.ACL} {
		if want := golden(t, name); got != want {
			t.Errorf("%s with a copy on the server differs from golden/%s.txt\n--- got\n%s--- want\n%s", name, name, got, want)
		}
	}
	var warned []finding.Finding
	for _, fi := range res.Findings {
		if fi.Reason == "an instance's stamps were made for another object (a copy?)" {
			warned = append(warned, fi)
		}
	}
	if len(warned) != 1 || warned[0].Severity != finding.Warning || warned[0].Subject != "instance/whoami-copy" {
		t.Errorf("want one warning about the copy, got %v (all findings: %v)", warned, res.Findings)
	}
}

func TestAnInstanceWithNoResourceStampYetHasItsStampsIgnoredWithOneFinding(t *testing.T) {
	f := serverOf(t, load(t))
	stampedLikeApplyDid(f)
	old := copyOf(f, "whoami", "whoami-old")
	delete(old.Config, ownership.ResourceKey) // made by an older tink: this stack's owner stamp and nothing else

	res := fromServer(t, f)
	if res.Errors() != 0 {
		t.Fatalf("an instance with no resource stamp blocked the join: %s", messages(res, finding.Error))
	}
	n := 0
	for _, fi := range res.Findings {
		if fi.Reason == "an instance has no resource stamp yet" && fi.Subject == "instance/whoami-old" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want one finding for it, got %d: %v", n, res.Findings)
	}
	if res.DNS != golden(t, "dns") {
		t.Errorf("its publish stamps were read:\n%s", res.DNS)
	}
}

// What is ignored is the stamps and no more: an instance made before stamps (no owner stamp) and one of another stack are read as they were, and an
// instance that carries none of the stamps the join reads is not reported for being there.
func TestOnlyACopyOrAnUnboundInstanceIsIgnoredAndNothingElseIsReported(t *testing.T) {
	f := serverOf(t, load(t)) // no stamps at all: the state of every host before this change
	if res := fromServer(t, f); len(res.Findings) != 0 {
		t.Errorf("an instance no stack stamped is read as it was: %v", res.Findings)
	}
	stampedLikeApplyDid(f)
	if res := fromServer(t, f); len(res.Findings) != 0 {
		t.Errorf("an instance whose stamps are its own is read as it was: %v", res.Findings)
	}
	// a copy of an object another stack stamped is a copy all the same: the stamps are compared with the object's own address, whoever wrote them
	other := copyOf(f, "whoami", "theirs")
	other.Config[ownership.Key] = "other"
	if res := fromServer(t, f); res.Errors() != 0 || !strings.Contains(messages(res, finding.Warning), "instance/theirs: (project canary) it carries the stamps of another instance") {
		t.Errorf("a copy of another stack's instance: %v", res.Findings)
	}
	// a copy that carries nothing the join reads
	f2 := serverOf(t, load(t))
	stampedLikeApplyDid(f2)
	bare := copyOf(f2, "whoami", "bare")
	for k := range bare.Config {
		if strings.HasPrefix(k, "user.tink.intent.") && k != ownership.Key && k != ownership.ResourceKey {
			delete(bare.Config, k)
		}
	}
	bare.Config[ownership.ResourceKey] = "instance/default/whoami"
	for _, fi := range fromServer(t, f2).Findings {
		if fi.Reason == "an instance's stamps were made for another object (a copy?)" {
			t.Errorf("a copy with nothing to ignore is not reported: %v", fi)
		}
	}
}

// The platform view (what the derive stage joins a stack to) says it too, for a helper; an app is not read there at all.
func TestThePlatformViewOnlyReportsWhatItReads(t *testing.T) {
	f := serverOf(t, load(t))
	stampedLikeApplyDid(f)
	copyOf(f, "whoami", "whoami-copy")
	stacks, err := PlatformFromServer(f, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks[0].Ignored) != 0 {
		t.Errorf("an app is not read by the platform view, so its stamps are not reported: %v", stacks[0].Ignored)
	}
}

// Overlay: the stack's own declaration of an instance replaces what the server held of it, so a finding about that instance goes with it.
func TestOverlayDropsTheFindingOfAnInstanceItDeclares(t *testing.T) {
	base := []Stack{{Name: "server", Ignored: []IgnoredStamps{{Project: "default", Instance: "a"}, {Project: "default", Instance: "b"}}}}
	over := []Stack{{Name: "stack", Resources: []resolve.Resource{{Kind: resolve.KindInstance, Name: "a"}}}}
	got := Overlay(base, over)
	if len(got[0].Ignored) != 1 || got[0].Ignored[0].Instance != "b" {
		t.Errorf("ignored = %+v, want only b's", got[0].Ignored)
	}
}

// The derived-file reconcile: a copy of an instance that carries a derived file is not written to and not restarted, and the original's file is kept.
func TestACopyOfAnInstanceThatCarriesADerivedFileIsNotWrittenToOrRestarted(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(true)))
	stampedLikeApplyDid(f)
	// a copy of the instance, without the role it states as a person (the copy of a helper is a separate matter: its role is a fact, and is still read)
	copyOf(f, "ingress", "ingress-copy", resolve.RoleKey)
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell
	files.files[fkey("tink", "ingress-copy", routesPath)] = emptyShell

	outs := reconcileDerived(f, files, rs)
	o := one(t, outs, "ingress-routes")
	if o.State != observe.Converged || !o.Wrote {
		t.Fatalf("the original's file was not kept: %+v", o)
	}
	if len(outs) != 1 {
		t.Errorf("one file, one outcome: %+v", outs)
	}
	if len(rs.calls) != 1 || rs.calls[0] != "tink/ingress" {
		t.Errorf("restarted %v: only the original", rs.calls)
	}
	if files.files[fkey("tink", "ingress-copy", routesPath)] != emptyShell {
		t.Error("the copy's file was written")
	}
	said := 0
	for _, fi := range o.Detail {
		if line := fi.String(); strings.Contains(line, "instance/ingress-copy") && strings.Contains(line, "ignored") {
			said++
		}
	}
	if said != 1 {
		t.Errorf("the copy is reported once, not %d times: %v", said, o.Detail)
	}
	// and the read on its own says the same, once
	ds, bad, ignored, err := DerivedFromServer(f, "")
	if err != nil || len(bad) != 0 || len(ds) != 1 || len(ignored) != 1 || ignored[0].Subject != "instance/ingress-copy" {
		t.Errorf("files %v problems %v findings %v err %v", ds, bad, ignored, err)
	}
}

// With nothing but a copy carrying the stamp, there is no file to keep, and the pass says why instead of saying nothing.
func TestAPassWhoseOnlyDerivedStampIsACopysSaysSo(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(true)))
	stampedLikeApplyDid(f)
	copyOf(f, "ingress", "ingress-copy", resolve.RoleKey)
	for i := range f.instances {
		if f.instances[i].Name == "ingress" {
			delete(f.instances[i].Config, resolve.DeriveStampKey("ingress-routes")) // the original lost it, so only the copy carries one
		}
	}
	outs := reconcileDerived(f, platformFiles(), &fakeRestarter{})
	if len(outs) != 1 || outs[0].State != observe.Skipped || len(outs[0].Detail) != 1 {
		t.Errorf("outcomes = %+v, want skipped with the copy's finding", outs)
	}
}
