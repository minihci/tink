package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/join"
)

// namedJoinFixture is the join's platform or canary stack with `site: NAME` added to its kind: stack document and nothing else changed.
func namedJoinFixture(t *testing.T, dir, src, header, site string) string {
	t.Helper()
	s := readJoinFixture(t, src)
	if !strings.Contains(s, header) {
		t.Fatalf("%s has no %q", src, header)
	}
	p := filepath.Join(dir, filepath.Base(src))
	if err := os.WriteFile(p, []byte(strings.Replace(s, header, header+"site: "+site+"\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runJoinSite(t *testing.T, args []string, site siteFlag) (stdout, stderr string, err error) {
	t.Helper()
	var out, errw bytes.Buffer
	err = runJoin(&out, &errw, args, "", site)
	return out.String(), errw.String(), err
}

// Files that name one site are read as that site without saying so: the join prints what it printed before sites.
func TestJoin_FilesThatNameOneSiteAreReadAsThatSite(t *testing.T) {
	dir := t.TempDir()
	platform := namedJoinFixture(t, dir, joinPlatform, "name: homelab-platform\n", "herd")
	canary := namedJoinFixture(t, dir, joinCanary, "name: canary\n", "herd")
	named, errw, err := runJoinSite(t, []string{platform, canary}, siteFlag{})
	if err != nil {
		t.Fatalf("%v\n%s", err, errw)
	}
	plain, _, err := runJoinSite(t, []string{joinPlatform, joinCanary}, siteFlag{})
	if err != nil {
		t.Fatal(err)
	}
	if named != plain {
		t.Errorf("a named site prints something else than the unnamed one does:\n%s\n---\n%s", named, plain)
	}
}

// Files of two sites are not guessed at, and --site chooses, including the unnamed site with an empty value.
func TestJoin_FilesOfSeveralSitesNeedASite(t *testing.T) {
	dir := t.TempDir()
	platform := namedJoinFixture(t, dir, joinPlatform, "name: homelab-platform\n", "herd")
	canary := namedJoinFixture(t, dir, joinCanary, "name: canary\n", "herd")
	files := []string{platform, canary, joinPlatform, joinCanary} // the same stacks twice: once in site herd, once in no site

	_, _, err := runJoinSite(t, files, siteFlag{})
	if err == nil || !strings.Contains(err.Error(), "more than one site") || !strings.Contains(err.Error(), "--site") {
		t.Fatalf("err = %v", err)
	}
	herd, errw, err := runJoinSite(t, files, siteFlag{"herd", true})
	if err != nil {
		t.Fatalf("%v\n%s", err, errw)
	}
	unnamed, errw, err := runJoinSite(t, files, siteFlag{"", true})
	if err != nil {
		t.Fatalf("%v\n%s", err, errw)
	}
	plain, _, _ := runJoinSite(t, []string{joinPlatform, joinCanary}, siteFlag{})
	if herd != plain || unnamed != plain {
		t.Errorf("a site cut from files of several is not what its own files print")
	}
	// a site nobody is in has no helpers: the join says so and prints nothing
	if out, _, err := runJoinSite(t, files, siteFlag{"nowhere", true}); err != nil || strings.Contains(out, "== dns") {
		t.Errorf("site nowhere: out %q, err %v", out, err)
	}
}

func TestJoin_ApplyTakesTheSiteTheSameWay(t *testing.T) {
	dir := t.TempDir()
	platform := namedJoinFixture(t, dir, joinPlatform, "name: homelab-platform\n", "herd")
	canary := namedJoinFixture(t, dir, joinCanary, "name: canary\n", "herd")
	files := []string{platform, canary, joinPlatform, joinCanary}
	var out, errw bytes.Buffer
	err := runJoinApply(&out, &errw, nil, "dns", files, siteFlag{}, join.ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "more than one site") {
		t.Errorf("err = %v: apply must not guess a site either", err)
	}
}
