package opinion

import (
	"strings"
	"testing"
)

func TestStorageJudgesByDriverAndHonoursAReason(t *testing.T) {
	for _, d := range []string{"zfs", "btrfs", "truenas", "ceph"} {
		if f := CheckStorage("storage-volume/v", "p", d, ""); f.State != Met {
			t.Errorf("%s should meet the opinion: %+v", d, f)
		}
	}
	for _, d := range []string{"dir", "lvm", "someday"} {
		f := CheckStorage("storage-volume/v", "scratch", d, "")
		if f.State != Departs || !strings.Contains(f.Message, `pool "scratch"`) || !strings.Contains(f.Message, "accept:") {
			t.Errorf("%s should depart and say how to accept it: %+v", d, f)
		}
		if f := CheckStorage("storage-volume/v", "scratch", d, "regenerable cache"); f.State != Accepted || f.Reason != "regenerable cache" {
			t.Errorf("%s with a reason is accepted, and keeps the reason: %+v", d, f)
		}
	}
	// a good driver needs no reason, and an unneeded one does not turn it into 'accepted': the opinion was met
	if f := CheckStorage("storage-volume/v", "p", "zfs", "because"); f.State != Met {
		t.Errorf("%+v", f)
	}
}

func TestIdentityAppliesOnlyToRegisteredInstances(t *testing.T) {
	if _, applies := CheckIdentity("instance/db", map[string]string{"boot.autostart": "true"}, ""); applies {
		t.Error("an instance that does not register with the ingress is not public: the opinion does not apply")
	}
	reg := map[string]string{"user.ingress.enabled": "true", "user.ingress.domain": "ns.example.com", "user.ingress.port": "1337"}
	f, applies := CheckIdentity("instance/nightscout", reg, "")
	if !applies || f.State != Departs || !strings.Contains(f.Message, "ns.example.com") || !strings.Contains(f.Message, "user.ingress.auth") {
		t.Errorf("%+v", f)
	}
	if f, _ := CheckIdentity("instance/nightscout", reg, "public status page"); f.State != Accepted || f.Reason != "public status page" {
		t.Errorf("%+v", f)
	}
	reg["user.ingress.auth"] = IngressAuth
	if f, _ := CheckIdentity("instance/nightscout", reg, ""); f.State != Met {
		t.Errorf("%+v", f)
	}
	reg["user.ingress.auth"] = "authentik"
	if f, _ := CheckIdentity("instance/nightscout", reg, ""); f.State != Departs || !strings.Contains(f.Message, `"authentik"`) {
		t.Errorf("a value tink does not know is not 'met': %+v", f)
	}
}

func TestEveryOpinionIsNamedAndFindable(t *testing.T) {
	for _, o := range All {
		if o.Default == "" || o.Why == "" || o.Alternative == "" || o.FixCost == "" {
			t.Errorf("an opinion states its default, its reason, its alternative and its fix cost: %+v", o)
		}
		if got, ok := Lookup(o.Name); !ok || got.Name != o.Name || !Known(string(o.Name)) {
			t.Errorf("%s", o.Name)
		}
	}
	if Known("nonsense") {
		t.Error("nonsense")
	}
}
