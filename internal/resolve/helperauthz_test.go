package resolve

import (
	"errors"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/helper"
)

// fakeAuthz is the narrowing of the daemon's certificate, as planning and applying are handed it.
type fakeAuthz struct {
	state   AuthzState
	planErr error
	applied []helper.Found
	applyOK error
}

func (f *fakeAuthz) PlanAuthz(_ incus.InstanceServer, _ helper.Found) (AuthzState, error) {
	return f.state, f.planErr
}

func (f *fakeAuthz) ApplyAuthz(_ incus.InstanceServer, h helper.Found) error {
	f.applied = append(f.applied, h)
	return f.applyOK
}

func authzServer(t *testing.T) *helperKindServer {
	t.Helper()
	return &helperKindServer{instances: []api.Instance{helperInstance(t, "tink-helper", "helper", "Running", goodStatus())}}
}

func narrowRes() Resource { return helperRes(func(r *Resource) { r.Authz = AuthzNarrow }) }

func planWith(t *testing.T, srv *helperKindServer, r Resource, a HelperAuthz) PlannedResource {
	t.Helper()
	opts := PlanOptions{}.WithHelperAuthz(a).ForResources([]Resource{r})
	p, err := planHelper(srv, r, helperNow, opts)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanSaysHowTheCertificateStandsWhenTheStackSaysNothing(t *testing.T) {
	cases := []struct {
		name         string
		state        AuthzState
		note         string
		wantWarnings bool
	}{
		{"unrestricted", AuthzState{Rules: 4}, "certificate is unrestricted", false},
		{"narrowed", AuthzState{Narrowed: true, UpToDate: true, Rules: 4}, "narrowed and the stack does not say so", false},
		{"narrowed, rules out of date", AuthzState{Narrowed: true, Rules: 4}, "narrowed and the stack does not say so", true},
		{"a scriptlet that is not tink's", AuthzState{Refusal: "authorization.scriptlet holds a scriptlet tink did not generate"}, "not narrowed by tink: authorization.scriptlet holds", false},
	}
	for _, c := range cases {
		p := planWith(t, authzServer(t), helperRes(nil), &fakeAuthz{state: c.state})
		if p.Action != ActionNone || len(p.Blocked) != 0 {
			t.Errorf("%s: asking nothing changes nothing: %+v", c.name, p)
		}
		if !strings.Contains(joined(p.Notes), c.note) {
			t.Errorf("%s: notes = %q, want %q", c.name, joined(p.Notes), c.note)
		}
		if got := strings.Contains(joined(p.Warnings), "no longer match"); got != c.wantWarnings {
			t.Errorf("%s: warnings = %q", c.name, joined(p.Warnings))
		}
	}
}

func TestPlanDoesNotAskAboutTheCertificateWhenNoneWasHandedOver(t *testing.T) {
	p := planWith(t, authzServer(t), helperRes(nil), nil)
	if strings.Contains(joined(p.Notes), "certificate") || len(p.Warnings) != 0 {
		t.Errorf("a plan that was not given the narrowing says nothing of it: %+v", p)
	}
}

func TestPlanCannotTellIsAWarningWhenNothingWasAsked(t *testing.T) {
	p := planWith(t, authzServer(t), helperRes(nil), &fakeAuthz{planErr: errors.New("the certificate is restricted")})
	if p.Action != ActionNone || !strings.Contains(joined(p.Warnings), "cannot tell whether the daemon's certificate is narrowed: the certificate is restricted") {
		t.Errorf("plan = %+v", p)
	}
}

func TestPlanNarrowsWhenAskedAndSaysNoChangesAfterwards(t *testing.T) {
	p := planWith(t, authzServer(t), narrowRes(), &fakeAuthz{state: AuthzState{Rules: 4}})
	if p.Action != ActionUpdate || !strings.Contains(joined(p.Changes), "narrow the daemon's certificate") || !strings.Contains(joined(p.Changes), "4 rules") {
		t.Errorf("unrestricted and asked: %+v", p)
	}
	p = planWith(t, authzServer(t), narrowRes(), &fakeAuthz{state: AuthzState{Narrowed: true, UpToDate: true, Rules: 1}})
	if p.Action != ActionNone || !strings.Contains(joined(p.Notes), "narrowed (1 rule)") {
		t.Errorf("narrowed and asked is a second plan with no changes: %+v", p)
	}
}

func TestPlanRewritesTheRulesWhenTheyAreOutOfDate(t *testing.T) {
	p := planWith(t, authzServer(t), narrowRes(), &fakeAuthz{state: AuthzState{Narrowed: true, Rules: 6}})
	if p.Action != ActionUpdate || !strings.Contains(joined(p.Changes), "rewrite the authorization scriptlet") || !strings.Contains(joined(p.Changes), "6 rules") {
		t.Errorf("plan = %+v", p)
	}
}

func TestPlanBlocksNarrowingWhatItMustNotChange(t *testing.T) {
	refused := &fakeAuthz{state: AuthzState{Refusal: "authorization.client.tls is \"openfga\""}}
	p := planWith(t, authzServer(t), narrowRes(), refused)
	if p.Action != ActionBlocked || !strings.Contains(joined(p.Blocked), "openfga") {
		t.Errorf("a route someone chose: %+v", p)
	}
	p = planWith(t, authzServer(t), narrowRes(), &fakeAuthz{planErr: errors.New("no certificate named \"tink-helper\" is trusted")})
	if p.Action != ActionBlocked || !strings.Contains(joined(p.Blocked), "no certificate named") {
		t.Errorf("no certificate to narrow: %+v", p)
	}
	p = planWith(t, authzServer(t), narrowRes(), nil)
	if p.Action != ActionBlocked || !strings.Contains(joined(p.Blocked), "tink bug") {
		t.Errorf("asked of a tink that cannot do it: %+v", p)
	}
}

func TestPlanBlocksTwoHelpersAskingForTheOneAuthorizer(t *testing.T) {
	first := narrowRes()
	second := helperRes(func(r *Resource) { r.Name, r.Project, r.Authz = "other", "elsewhere", AuthzNarrow })
	opts := PlanOptions{}.WithHelperAuthz(&fakeAuthz{}).ForResources([]Resource{first, second})
	for _, r := range []Resource{first, second} {
		p, err := planHelper(&helperKindServer{}, r, helperNow, opts)
		if err != nil {
			t.Fatal(err)
		}
		if p.Action != ActionBlocked || !strings.Contains(joined(p.Blocked), "also says authz: narrow") {
			t.Errorf("%s: %+v", r.Name, p)
		}
	}
	// one asking, one not: nothing is in the way
	quiet := helperRes(func(r *Resource) { r.Name, r.Project = "other", "elsewhere" })
	opts = PlanOptions{}.WithHelperAuthz(&fakeAuthz{}).ForResources([]Resource{first, quiet})
	if p, _ := planHelper(&helperKindServer{}, first, helperNow, opts); p.Action == ActionBlocked {
		t.Errorf("only one helper asks: %+v", p)
	}
}

func TestPlanAHelperThatIsNotThereYetNarrowsAfterTheInstall(t *testing.T) {
	p := planWith(t, &helperKindServer{}, narrowRes(), &fakeAuthz{})
	if p.Action != ActionCreate || !strings.Contains(joined(p.Changes), "then narrow its certificate") {
		t.Errorf("plan = %+v", p)
	}
	p = planWith(t, &helperKindServer{}, helperRes(nil), &fakeAuthz{})
	if strings.Contains(joined(p.Changes), "narrow") {
		t.Errorf("not asked: %+v", p)
	}
}

func TestCreateHelperNarrowsOnlyWhenAskedAndOnlyAfterTheInstall(t *testing.T) {
	var installed bool
	old := installHelper
	installHelper = func(incus.InstanceServer, helper.InstallOptions) error { installed = true; return nil }
	defer func() { installHelper = old }()

	srv := authzServer(t)
	quiet := &fakeAuthz{}
	if err := createHelper(srv, helperRes(nil), quiet); err != nil || len(quiet.applied) != 0 {
		t.Fatalf("not asked: %v %v", err, quiet.applied)
	}
	asked := &fakeAuthz{}
	if err := createHelper(srv, narrowRes(), asked); err != nil || !installed {
		t.Fatalf("asked: %v installed=%v", err, installed)
	}
	if len(asked.applied) != 1 || asked.applied[0].Label() != "tink-helper/helper" {
		t.Errorf("narrowed %+v", asked.applied)
	}

	failing := &fakeAuthz{applyOK: errors.New("the server refused")}
	err := createHelper(srv, narrowRes(), failing)
	if err == nil || !strings.Contains(err.Error(), "is installed, and its certificate is not narrowed") || !strings.Contains(err.Error(), "the server refused") {
		t.Errorf("the install must not be reported as the whole story: %v", err)
	}
}

func TestUpdateOneNarrowsTheHelperThatIsThere(t *testing.T) {
	a := &fakeAuthz{}
	if err := updateOne(authzServer(t), narrowRes(), volumeEnv{helperAuthz: a}); err != nil {
		t.Fatal(err)
	}
	if len(a.applied) != 1 || a.applied[0].Name != "helper" {
		t.Errorf("applied %+v", a.applied)
	}
	if err := updateOne(&helperKindServer{}, narrowRes(), volumeEnv{helperAuthz: a}); err == nil || !strings.Contains(err.Error(), "no helper by that name") {
		t.Errorf("a helper that is gone: %v", err)
	}
	if err := updateOne(authzServer(t), narrowRes(), volumeEnv{}); err == nil || !strings.Contains(err.Error(), "tink bug") {
		t.Errorf("no narrowing handed over: %v", err)
	}
}

func TestAuthzLoadsOnAHelperAndNowhereElse(t *testing.T) {
	rs, err := loadStack(t, "kind: stack\nname: p\nproject: tink\n---\nkind: helper\nname: helper\nauthz: narrow\n")
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, r := range rs {
		if r.Kind == KindHelper {
			got = r.Authz
		}
	}
	if got != AuthzNarrow {
		t.Errorf("authz = %q", got)
	}
	if _, err := loadStack(t, "kind: stack\nname: p\nproject: tink\n---\nkind: helper\nname: helper\nauthz: restrict\n"); err == nil || !strings.Contains(err.Error(), `authz "restrict"`) || !strings.Contains(err.Error(), "narrow") {
		t.Errorf("an unknown value must say what is known: %v", err)
	}
	if _, err := loadStack(t, "kind: stack\nname: p\nproject: tink\n---\nkind: profile\nname: x\nauthz: narrow\n"); err == nil || !strings.Contains(err.Error(), `does not use field "Authz"`) {
		t.Errorf("authz on a profile: %v", err)
	}
}
