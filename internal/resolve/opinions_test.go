package resolve

import (
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/opinion"
)

type poolsServer struct {
	incus.InstanceServer
	drivers map[string]string
}

func (s poolsServer) GetStoragePool(name string) (*api.StoragePool, string, error) {
	d, ok := s.drivers[name]
	if !ok {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "pool not found")
	}
	return &api.StoragePool{Name: name, Driver: d}, "", nil
}

func find(t *testing.T, fs []opinion.Finding, o opinion.Name, res string) opinion.Finding {
	t.Helper()
	for _, f := range fs {
		if f.Opinion == o && f.Resource == res {
			return f
		}
	}
	t.Fatalf("no %s finding for %s in %+v", o, res, fs)
	return opinion.Finding{}
}

func TestOpinionsJudgeAStackAgainstWhatTheServerHas(t *testing.T) {
	srv := poolsServer{drivers: map[string]string{"default": "btrfs", "scratch": "dir"}}
	none := &backupmeta.VolumeBackup{None: "regenerable"}
	resources := []Resource{
		{Kind: KindStorageVolume, Name: "library", Backup: none},
		{Kind: KindStorageVolume, Name: "cache", Pool: "scratch", Backup: none, Accept: map[string]string{"storage": "regenerable cache"}},
		{Kind: KindStorageVolume, Name: "scratch-db", Pool: "scratch"},
		{Kind: KindStorageVolume, Name: "ghost", Pool: "nowhere", Backup: none},
		{Kind: KindInstance, Name: "internal-db", Config: map[string]string{"boot.autostart": "true"}},
		{Kind: KindInstance, Name: "nightscout", Config: map[string]string{"user.ingress.enabled": "true", "user.ingress.domain": "ns.example.com"}},
		{Kind: KindInstance, Name: "status", Config: map[string]string{"user.ingress.enabled": "true", "user.ingress.domain": "s.example.com"},
			Accept: map[string]string{"identity": "public status page"}},
		{Kind: KindInstance, Name: "wiki", Config: map[string]string{"user.ingress.enabled": "true", "user.ingress.auth": "authelia"}},
	}
	fs, err := Opinions(srv, resources)
	if err != nil {
		t.Fatal(err)
	}

	if f := find(t, fs, opinion.Storage, "storage-volume/library"); f.State != opinion.Met {
		t.Errorf("%+v", f)
	}
	if f := find(t, fs, opinion.Storage, "storage-volume/cache"); f.State != opinion.Accepted || f.Reason != "regenerable cache" {
		t.Errorf("%+v", f)
	}
	if f := find(t, fs, opinion.Storage, "storage-volume/scratch-db"); f.State != opinion.Departs {
		t.Errorf("%+v", f)
	}
	if f := find(t, fs, opinion.Storage, "storage-volume/ghost"); f.State != opinion.Departs || !strings.Contains(f.Message, "could not be read") {
		t.Errorf("a pool that is not there: %+v", f)
	}
	if f := find(t, fs, opinion.Backup, "storage-volume/scratch-db"); f.State != opinion.Departs {
		t.Errorf("a volume that does not say how it is backed up: %+v", f)
	}
	if f := find(t, fs, opinion.Backup, "storage-volume/library"); f.State != opinion.Accepted || f.Reason != "regenerable" {
		t.Errorf("backup: none: carries its reason: %+v", f)
	}
	for _, name := range []string{"nightscout", "status", "wiki"} {
		find(t, fs, opinion.Identity, "instance/"+name)
	}
	for _, f := range fs {
		if f.Resource == "instance/internal-db" {
			t.Errorf("an instance that does not register with the ingress is not public: %+v", f)
		}
	}
	if f := find(t, fs, opinion.Identity, "instance/nightscout"); f.State != opinion.Departs {
		t.Errorf("%+v", f)
	}
	if f := find(t, fs, opinion.Identity, "instance/status"); f.State != opinion.Accepted {
		t.Errorf("%+v", f)
	}
	if f := find(t, fs, opinion.Identity, "instance/wiki"); f.State != opinion.Met {
		t.Errorf("%+v", f)
	}

	sum := Summarise(fs)
	if len(sum) != 3 || sum[1].Opinion.Name != opinion.Storage || sum[1].N != 4 || sum[1].Met != 1 || sum[1].Accepted != 1 || sum[1].Departs != 2 {
		t.Errorf("%+v", sum)
	}
}

func TestAcceptNeedsARealOpinionAKindThatUsesItAndAReason(t *testing.T) {
	vol := func(a map[string]string) Resource { return Resource{Kind: KindStorageVolume, Name: "v", Accept: a} }
	inst := func(a map[string]string) Resource { return Resource{Kind: KindInstance, Name: "i", Accept: a} }
	for name, tc := range map[string]struct {
		r    Resource
		want string
	}{
		"unknown opinion":   {vol(map[string]string{"colour": "blue"}), "not an opinion tink has"},
		"backup elsewhere":  {vol(map[string]string{"backup": "no"}), "backup: none: REASON"},
		"storage on a box":  {inst(map[string]string{"storage": "x"}), "applies to a storage-volume"},
		"identity on a vol": {vol(map[string]string{"identity": "x"}), "applies to an instance"},
		"no reason":         {vol(map[string]string{"storage": "  "}), "needs a reason"},
	} {
		if err := Validate(tc.r); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Validate(vol(map[string]string{"storage": "scratch"})); err != nil {
		t.Error(err)
	}
	if err := Validate(Resource{Kind: KindProfile, Name: "p", Accept: map[string]string{"storage": "x"}}); err == nil {
		t.Error("only volumes and instances accept")
	}
}
