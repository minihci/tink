package derive

import (
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/join"
	"github.com/minihci/tink/internal/ownership"
	"github.com/minihci/tink/internal/resolve"
)

// stackOf is a small stack that asks for a bridge of its own: an app with an offer, and a database that has none.
func stackOf(name string, opts ...func(*resolve.Resource)) []resolve.Resource {
	st := resolve.Resource{Kind: resolve.KindStack, Name: name, Project: "p-" + name, Network: resolve.StackNetworkOwn}
	for _, o := range opts {
		o(&st)
	}
	return []resolve.Resource{
		st,
		{Kind: resolve.KindInstance, Name: "app", Project: "p-" + name, Image: "x", Profiles: []string{"default"},
			Config: map[string]string{"user.tink.offer.web.port": "80", "user.tink.offer.web.proto": "http"}},
		{Kind: resolve.KindInstance, Name: "db", Project: "p-" + name, Image: "y", Profiles: []string{"default"}},
		{Kind: resolve.KindPublish, Name: name, Project: "p-" + name, Instance: "app", Offer: "web", Allow: []string{"family"}},
	}
}

func find(rs []resolve.Resource, kind resolve.Kind, name string) *resolve.Resource {
	for i := range rs {
		if rs[i].Kind == kind && rs[i].Name == name {
			return &rs[i]
		}
	}
	return nil
}

// A stack that asks for a bridge gets one, its ACL, and the NIC and address of what is published; the database gets a NIC and no address.
func TestOwnNetworkMakesTheBridgeTheACLAndTheAddresses(t *testing.T) {
	got, notices, err := OwnNetwork(&serverFake{}, stackOf("blog"))
	if err != nil {
		t.Fatal(err)
	}
	nw := find(got, resolve.KindNetwork, "tink-blog")
	if nw == nil {
		t.Fatalf("no network was made: %+v", got)
	}
	gw, err := netip.ParsePrefix(nw.Config["ipv4.address"])
	if err != nil {
		t.Fatalf("ipv4.address = %q", nw.Config["ipv4.address"])
	}
	if gw.Bits() != 24 || !netip.MustParsePrefix("10.0.0.0/8").Contains(gw.Addr()) || gw.Addr() != gw.Masked().Addr().Next() {
		t.Errorf("the subnet is a /24 of 10.0.0.0/8 with the gateway first: %s", gw)
	}
	if nw.Config["ipv4.nat"] != "true" || nw.Config["ipv6.address"] != "none" || nw.Config["security.acls"] != "tink-blog-in" {
		t.Errorf("config = %v", nw.Config)
	}
	// 254 hosts: the gateway, then 63 static addresses (.2 to .64), then DHCP from .65
	base := gw.Masked().Addr()
	wantDHCP := base.Next().Next()
	for i := 0; i < 63; i++ {
		wantDHCP = wantDHCP.Next()
	}
	if !strings.HasPrefix(nw.Config["ipv4.dhcp.ranges"], wantDHCP.String()+"-") {
		t.Errorf("dhcp range = %q, want it to start at %s", nw.Config["ipv4.dhcp.ranges"], wantDHCP)
	}
	acl := find(got, resolve.KindNetworkACL, "tink-blog-in")
	if acl == nil || !acl.DeriveIngress {
		t.Fatalf("the ACL was not made, or does not derive its rules: %+v", acl)
	}
	app := find(got, resolve.KindInstance, "app")
	nic := app.Devices["eth0"]
	if nic["type"] != "nic" || nic["network"] != "tink-blog" || nic["name"] != "eth0" {
		t.Errorf("app NIC = %v", nic)
	}
	if want := base.Next().Next().String(); nic["ipv4.address"] != want {
		t.Errorf("the app's address = %q, want the first static address %s", nic["ipv4.address"], want)
	}
	db := find(got, resolve.KindInstance, "db")
	if d := db.Devices["eth0"]; d["network"] != "tink-blog" || d["ipv4.address"] != "" {
		t.Errorf("db NIC = %v: on the bridge, and no static address because nothing is published", d)
	}
	if len(notices) != 2 || !strings.Contains(notices[0], "bridge tink-blog") || !strings.Contains(notices[1], "instance/app: address") {
		t.Errorf("notices = %v", notices)
	}
}

// Once the bridge and the address are on the server, a second plan reads them back and derives the same stack: nothing is chosen again.
func TestOwnNetworkIsStable(t *testing.T) {
	first, _, err := OwnNetwork(&serverFake{}, stackOf("blog"))
	if err != nil {
		t.Fatal(err)
	}
	nw := find(first, resolve.KindNetwork, "tink-blog")
	app := find(first, resolve.KindInstance, "app")
	// the server as apply leaves it: the bridge (with its stamp) and the instance with its address
	f := &serverFake{networks: []api.Network{{Name: "tink-blog", Managed: true, NetworkPut: api.NetworkPut{Config: map[string]string{
		"ipv4.address": nw.Config["ipv4.address"], ownership.Key: "blog"}}}},
		instances: []api.Instance{{Name: "app", Project: "p-blog", InstancePut: api.InstancePut{Devices: app.Devices}}}}
	second, notices, err := OwnNetwork(f, stackOf("blog"))
	if err != nil {
		t.Fatal(err)
	}
	if got := find(second, resolve.KindNetwork, "tink-blog").Config; got["ipv4.address"] != nw.Config["ipv4.address"] || got["ipv4.dhcp.ranges"] != nw.Config["ipv4.dhcp.ranges"] {
		t.Errorf("the bridge changed: %v then %v", nw.Config, got)
	}
	if got := find(second, resolve.KindInstance, "app").Devices["eth0"]["ipv4.address"]; got != app.Devices["eth0"]["ipv4.address"] {
		t.Errorf("the address changed from %s to %s", app.Devices["eth0"]["ipv4.address"], got)
	}
	if !strings.Contains(notices[0], "(exists)") || !strings.Contains(notices[1], "kept") {
		t.Errorf("notices = %v", notices)
	}
}

// A subnet that another network holds is not chosen; a static address another NIC holds, in any project, is not either.
func TestOwnNetworkStaysClearOfWhatTheServerHolds(t *testing.T) {
	first, _, _ := OwnNetwork(&serverFake{}, stackOf("blog"))
	taken := netip.MustParsePrefix(find(first, resolve.KindNetwork, "tink-blog").Config["ipv4.address"]).Masked()
	f := &serverFake{networks: []api.Network{{Name: "other", Managed: true, NetworkPut: api.NetworkPut{Config: map[string]string{"ipv4.address": taken.Addr().Next().String() + "/24"}}}}}
	got, _, err := OwnNetwork(f, stackOf("blog"))
	if err != nil {
		t.Fatal(err)
	}
	chosen := netip.MustParsePrefix(find(got, resolve.KindNetwork, "tink-blog").Config["ipv4.address"]).Masked()
	if chosen.Overlaps(taken) {
		t.Errorf("chose %s, which overlaps %s", chosen, taken)
	}

	// the same bridge, already there, with its first static address held by an instance of another project
	nw := find(first, resolve.KindNetwork, "tink-blog")
	heldAddr := taken.Addr().Next().Next().String()
	f = &serverFake{
		networks: []api.Network{{Name: "tink-blog", Managed: true, NetworkPut: api.NetworkPut{Config: map[string]string{"ipv4.address": nw.Config["ipv4.address"]}}}},
		instances: []api.Instance{{Name: "stranger", Project: "elsewhere",
			ExpandedDevices: map[string]map[string]string{"eth0": {"type": "nic", "ipv4.address": heldAddr}}}},
	}
	got, _, err = OwnNetwork(f, stackOf("blog"))
	if err != nil {
		t.Fatal(err)
	}
	if a := find(got, resolve.KindInstance, "app").Devices["eth0"]["ipv4.address"]; a == heldAddr || a != taken.Addr().Next().Next().Next().String() {
		t.Errorf("address = %s: want the next free one after the held %s", a, heldAddr)
	}
}

// What the stack writes itself wins.
func TestOwnNetworkLeavesWhatTheStackWrote(t *testing.T) {
	rs := stackOf("blog")
	for i := range rs {
		if rs[i].Name == "app" && rs[i].Kind == resolve.KindInstance {
			rs[i].Devices = map[string]map[string]string{"eth0": {"type": "nic", "network": "lan0"}}
		}
		if rs[i].Name == "db" && rs[i].Kind == resolve.KindInstance {
			rs[i].Devices = map[string]map[string]string{"nic0": {"type": "nic", "network": "tink-blog", "ipv4.address": "10.9.9.9"}}
			rs[i].Config = map[string]string{"user.tink.offer.admin.port": "81", "user.tink.offer.admin.proto": "http"}
		}
	}
	got, _, err := OwnNetwork(&serverFake{}, rs)
	if err != nil {
		t.Fatal(err)
	}
	if d := find(got, resolve.KindInstance, "app").Devices["eth0"]; d["network"] != "lan0" || d["ipv4.address"] != "" {
		t.Errorf("a NIC that names a network is left alone: %v", d)
	}
	if d := find(got, resolve.KindInstance, "db").Devices["nic0"]; d["ipv4.address"] != "10.9.9.9" {
		t.Errorf("an address the stack wrote is kept: %v", d)
	}
}

func TestOwnNetworkUsesAStacksPoolAndPrefix(t *testing.T) {
	got, _, err := OwnNetwork(&serverFake{}, stackOf("blog", func(r *resolve.Resource) {
		r.NetworkPool, r.NetworkPrefix = "203.0.113.0/24", 27
	}))
	if err != nil {
		t.Fatal(err)
	}
	p := netip.MustParsePrefix(find(got, resolve.KindNetwork, "tink-blog").Config["ipv4.address"])
	if p.Bits() != 27 || !netip.MustParsePrefix("203.0.113.0/24").Contains(p.Addr()) {
		t.Errorf("subnet = %s, want a /27 of 203.0.113.0/24", p)
	}
	// a /27 has 30 hosts: the gateway, seven static addresses, then DHCP
	if want := p.Masked().Addr().Next().Next().String(); find(got, resolve.KindInstance, "app").Devices["eth0"]["ipv4.address"] != want {
		t.Errorf("address = %s, want %s", find(got, resolve.KindInstance, "app").Devices["eth0"]["ipv4.address"], want)
	}
}

func TestOwnNetworkRefusals(t *testing.T) {
	existing := func(owner string, managed bool, addr string) *serverFake {
		cfg := map[string]string{"ipv4.address": addr}
		if owner != "" {
			cfg[ownership.Key] = owner
		}
		return &serverFake{networks: []api.Network{{Name: "tink-blog", Managed: managed, NetworkPut: api.NetworkPut{Config: cfg}}}}
	}
	cases := []struct {
		name string
		srv  *serverFake
		want string
	}{
		{"another stack's bridge", existing("shop", true, "10.1.2.1/24"), `belongs to the stack "shop"`},
		{"a bridge Incus does not manage", existing("", false, ""), "does not manage"},
		{"a bridge with no address", existing("blog", true, "none"), "no IPv4 address"},
	}
	for _, c := range cases {
		if _, _, err := OwnNetwork(c.srv, stackOf("blog")); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.want)
		}
	}
	// a pool with no room
	rs := stackOf("blog", func(r *resolve.Resource) { r.NetworkPool, r.NetworkPrefix = "203.0.113.0/24", 24 })
	f := &serverFake{networks: []api.Network{{Name: "x", Managed: true, NetworkPut: api.NetworkPut{Config: map[string]string{"ipv4.address": "203.0.113.1/24"}}}}}
	if _, _, err := OwnNetwork(f, rs); err == nil || !strings.Contains(err.Error(), "network_pool") {
		t.Errorf("a full pool: err = %v", err)
	}
	// every static address held
	nw := "10.50.0.1/24"
	f = &serverFake{networks: []api.Network{{Name: "tink-blog", Managed: true, NetworkPut: api.NetworkPut{Config: map[string]string{"ipv4.address": nw}}}}}
	devs := map[string]map[string]string{}
	for i := 2; i < 2+63; i++ {
		devs["n"+strings.Repeat("x", i)] = map[string]string{"type": "nic", "ipv4.address": "10.50.0." + strconv.Itoa(i)}
	}
	f.instances = []api.Instance{{Name: "crowd", Project: "elsewhere", ExpandedDevices: devs}}
	if _, _, err := OwnNetwork(f, stackOf("blog")); err == nil || !strings.Contains(err.Error(), "no free static address") {
		t.Errorf("no room: err = %v", err)
	}
}

// No `network: own`, no work: the server is not read and the resources are the same.
func TestOwnNetworkAsksNothingOfAStackThatWantsNothing(t *testing.T) {
	rs := stackOf("blog", func(r *resolve.Resource) { r.Network = "" })
	f := &serverFake{}
	got, notices, err := OwnNetwork(f, rs)
	if err != nil || notices != nil || f.calls != 0 || len(got) != len(rs) {
		t.Errorf("got %d resources, notices %v, err %v, server calls %d", len(got), notices, err, f.calls)
	}
	if find(got, resolve.KindInstance, "app").Devices != nil {
		t.Error("an instance must be left as written")
	}
}

func TestBridgeNameFitsAnInterfaceName(t *testing.T) {
	for _, s := range []string{"a", "blog", "ten-chars-x", "a-very-long-stack-name-indeed", "metrics.production_v2"} {
		n := BridgeName(s)
		if len(n) > 15 || !strings.HasPrefix(n, BridgePrefix) {
			t.Errorf("%q -> %q", s, n)
		}
	}
	if BridgeName("blog") != "tink-blog" {
		t.Error("a short name is kept whole")
	}
	if BridgeName("a-very-long-stack-name-indeed") == BridgeName("a-very-long-stack-name-instead") {
		t.Error("two long names that share a start must not meet")
	}
}

// The bridge, the addresses and then the ingress rules, against the platform of the join's fixtures. The ACL's rule is for the app's port,
// admitting the ingress helper, and the whole stack is one the join accepts with no error.
func TestTheRulesFollowTheBridge(t *testing.T) {
	srv := platformOnServer(t)
	got, notices, err := OwnNetwork(srv, stackOf("blog"))
	if err != nil {
		t.Fatal(err)
	}
	more, err := ACLRules(srv, got) // the order prepare runs them in: the bridge and the addresses first
	if err != nil {
		t.Fatal(err)
	}
	notices = append(notices, more...)
	acl := find(got, resolve.KindNetworkACL, "tink-blog-in")
	if acl == nil || len(acl.Ingress) != 1 || acl.Ingress[0].DestinationPort != "80" || acl.Ingress[0].Source != "192.0.2.3/32" {
		t.Fatalf("ACL = %+v", acl)
	}
	if len(notices) != 3 {
		t.Errorf("notices = %v", notices)
	}
	platform, err := join.PlatformFromServer(srv)
	if err != nil {
		t.Fatal(err)
	}
	if res := join.Build(join.Overlay(platform, []join.Stack{{Name: "blog", Resources: got}})); res.Errors() != 0 {
		t.Errorf("the join does not accept what was derived: %v", res.Findings)
	}
}

// With no server, a pass that has something to derive says that it needs one, and one that has nothing to derive does not mind.
func TestPassesWithNoServer(t *testing.T) {
	if _, _, err := OwnNetwork(nil, stackOf("blog")); err == nil || !strings.Contains(err.Error(), "none is connected") {
		t.Errorf("network: own: err = %v", err)
	}
	if _, err := ACLRules(nil, canary(t)); err == nil || !strings.Contains(err.Error(), "none is connected") {
		t.Errorf("derive_ingress: err = %v", err)
	}
	if _, err := DerivedFiles(nil, site(t)); err == nil || !strings.Contains(err.Error(), "none is connected") {
		t.Errorf("derived files: err = %v", err)
	}
	plain := stackOf("blog", func(r *resolve.Resource) { r.Network = "" })
	if _, _, err := OwnNetwork(nil, plain); err != nil {
		t.Errorf("a stack that asks for nothing needs no server: %v", err)
	}
}
