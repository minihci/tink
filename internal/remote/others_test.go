package remote

import (
	"reflect"
	"testing"
)

func TestWhenATokenListsSeveralAddressesTheOthersAreReported(t *testing.T) {
	f := newFake(t, false, "s3cret", "default")
	conf, _ := emptyConf(t)
	tok := tokenAt(t, "s3cret", f.fingerprint(), "127.0.0.1:1", f.addr(), "127.0.0.1:2")
	res, err := Add(conf, AddOptions{Name: "tron", Token: tok, fetchCert: fetchWithDead(map[string]string{"https://127.0.0.1:1": "down"})})
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://" + f.addr(); res.Addr != want {
		t.Fatalf("the first address that answers is still the one kept: got %s, want %s", res.Addr, want)
	}
	// every address the token listed that was not kept, in the order listed, including the one that never answered
	if want := []string{"https://127.0.0.1:1", "https://127.0.0.1:2"}; !reflect.DeepEqual(res.Others, want) {
		t.Errorf("Others = %v, want %v", res.Others, want)
	}
}

func TestAnAddressThePersonGaveIsNotSecondGuessed(t *testing.T) {
	f := newFake(t, false, "s3cret", "default")
	conf, _ := emptyConf(t)
	tok := tokenAt(t, "s3cret", f.fingerprint(), f.addr(), "127.0.0.1:2")
	res, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	if res.Others != nil {
		t.Errorf("an explicit address needs no note about the token's other addresses, got %v", res.Others)
	}
}

func TestATokenWithOneAddressHasNoOthers(t *testing.T) {
	f := newFake(t, false, "s3cret", "default")
	conf, _ := emptyConf(t)
	res, err := Add(conf, AddOptions{Name: "tron", Token: tokenAt(t, "s3cret", f.fingerprint(), f.addr())})
	if err != nil {
		t.Fatal(err)
	}
	if res.Others != nil {
		t.Errorf("got %v", res.Others)
	}
}
