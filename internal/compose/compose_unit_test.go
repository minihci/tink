package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func imp(t *testing.T, src string, files map[string]string) *Result {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Import([]byte(src), Options{Dir: dir, Stack: "t"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return res
}

func refusals(res *Result) string {
	var b strings.Builder
	for _, r := range res.Refused {
		b.WriteString(r.String() + "\n")
	}
	return b.String()
}

// Every refusal is listed, not only the first: the point of a refusal is to tell the person all of what to change.
func TestRefusalsAreAllListed(t *testing.T) {
	res := imp(t, "services:\n  a:\n    image: x\n    build: .\n    privileged: true\n  b:\n    image: y\n    network_mode: host\n", nil)
	got := refusals(res)
	for _, want := range []string{"service a: build:", "service a: privileged:", "service b: network_mode:"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if res.YAML != "" {
		t.Error("a refused file must write nothing")
	}
}

// A command without an entrypoint adds arguments to the image's entrypoint, which tink's oci.entrypoint would replace. An entrypoint alone is
// exactly oci.entrypoint, because compose drops the image's command when the entrypoint is set.
func TestCommandAndEntrypoint(t *testing.T) {
	if got := refusals(imp(t, "services:\n  a:\n    image: x\n    command: --flag\n", nil)); !strings.Contains(got, "service a: command:") {
		t.Errorf("command alone must be refused, got %q", got)
	}
	res := imp(t, "services:\n  a:\n    image: x\n    entrypoint: /cron.sh\n", nil)
	if len(res.Refused) > 0 || !strings.Contains(res.YAML, "oci.entrypoint: /cron.sh") {
		t.Errorf("an entrypoint alone must become oci.entrypoint:\n%s\n%s", refusals(res), res.YAML)
	}
	res = imp(t, "services:\n  a:\n    image: x\n    entrypoint: [/bin/app, --one]\n    command: [--two]\n", nil)
	if !strings.Contains(res.YAML, "oci.entrypoint: /bin/app --one --two") {
		t.Errorf("entrypoint and command must be joined:\n%s", res.YAML)
	}
	if got := refusals(imp(t, "services:\n  a:\n    image: x\n    entrypoint: sh -c 'echo hi'\n", nil)); !strings.Contains(got, "needs a shell") {
		t.Errorf("a quoted string needs a list, got %q", got)
	}
}

func TestInterpolation(t *testing.T) {
	files := map[string]string{".env": "SET=v1\nEMPTY=\nQUOTED=\"two words\"\n"}
	cases := []struct{ in, want string }{
		{"${SET}", "v1"},
		{"${UNSET:-d}", "d"},
		{"${UNSET-d}", "d"},
		{"${EMPTY:-d}", "d"},
		{"${EMPTY-d}", ""},
		{"${QUOTED}", "two words"},
		{"a$${SET}", "a${SET}"},
	}
	for _, c := range cases {
		res := imp(t, "services:\n  a:\n    image: x\n    environment:\n      K: '"+c.in+"'\n", files)
		want := "environment.K: " + scalar(c.want)
		if !strings.Contains(res.YAML, want) {
			t.Errorf("%s: want %q in:\n%s\n%s", c.in, want, res.YAML, refusals(res))
		}
	}
	for _, bad := range []string{"${UNSET}", "$SET", "${SET:?needed}"} {
		if got := refusals(imp(t, "services:\n  a:\n    image: x\n    environment:\n      K: '"+bad+"'\n", files)); got == "" {
			t.Errorf("%s must be refused", bad)
		}
	}
}

// The shell's environment is never read: a variable set only there is unset here.
func TestInterpolationIgnoresTheShell(t *testing.T) {
	t.Setenv("ONLY_IN_THE_SHELL", "leaked")
	res := imp(t, "services:\n  a:\n    image: x\n    environment:\n      K: ${ONLY_IN_THE_SHELL:-fallback}\n", nil)
	if !strings.Contains(res.YAML, "environment.K: fallback") {
		t.Errorf("the shell's environment must not be read:\n%s", res.YAML)
	}
}

func TestVolumes(t *testing.T) {
	res := imp(t, `services:
  web:
    image: x
    volumes:
      - data:/data
      - ./conf:/conf:ro
      - /srv/media:/media:ro
      - type: bind
        source: /srv/fonts
        target: /fonts
        read_only: true
volumes:
  data:
`, nil)
	if len(res.Refused) > 0 {
		t.Fatal(refusals(res))
	}
	for _, want := range []string{"name: data\n", "name: web-conf\n", "source: data", "source: web-conf", "source: /srv/media", "source: /srv/fonts", `readonly: "true"`} {
		if !strings.Contains(res.YAML, want) {
			t.Errorf("missing %q in:\n%s", want, res.YAML)
		}
	}
	for src, why := range map[string]string{"/var/run/docker.sock:/var/run/docker.sock": "Docker socket", "undeclared:/x": "not declared", "/only": "SOURCE:TARGET"} {
		got := refusals(imp(t, "services:\n  w:\n    image: x\n    volumes:\n      - "+src+"\n", nil))
		if !strings.Contains(got, why) {
			t.Errorf("%s: want a refusal containing %q, got %q", src, why, got)
		}
	}
}

// A service called actual_server cannot be an instance (no underscore), and a volume called like a service would be a duplicate name.
func TestNames(t *testing.T) {
	res := imp(t, "services:\n  actual_server:\n    image: x\n    volumes:\n      - actual-server:/d\nvolumes:\n  actual-server:\n", nil)
	if len(res.Refused) > 0 {
		t.Fatal(refusals(res))
	}
	if !strings.Contains(res.YAML, "kind: instance\nname: actual-server\n") || !strings.Contains(res.YAML, "kind: storage-volume\nname: actual-server-data\n") {
		t.Errorf("names not made distinct:\n%s", res.YAML)
	}
}

func TestEnvironmentWithoutValueIsRefused(t *testing.T) {
	for _, env := range []string{"environment:\n      - ONLY_A_NAME", "environment:\n      ONLY_A_NAME:"} {
		if got := refusals(imp(t, "services:\n  a:\n    image: x\n    "+env+"\n", nil)); !strings.Contains(got, "no value") {
			t.Errorf("%q must be refused, got %q", env, got)
		}
	}
}

func TestDroppedKeysAreWarnedAndCommented(t *testing.T) {
	res := imp(t, "services:\n  a:\n    image: x\n    healthcheck: {test: [CMD, true]}\n    cap_add: [NET_ADMIN]\n", nil)
	if len(res.Warnings) != 2 {
		t.Errorf("want 2 warnings, got %v", res.Warnings)
	}
	if !strings.Contains(res.YAML, "# healthcheck was dropped") || !strings.Contains(res.YAML, "# cap_add was dropped") {
		t.Errorf("each dropped key must be a comment in the output:\n%s", res.YAML)
	}
}

func TestSecretWarningSkipsSwitches(t *testing.T) {
	res := imp(t, "services:\n  a:\n    image: x\n    environment:\n      DB_PASSWORD: hunter2\n      RANDOM_ROOT_PASSWORD: '1'\n", nil)
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "DB_PASSWORD") {
		t.Errorf("only the real secret should be warned about: %v", res.Warnings)
	}
}

func TestUserOwnsTheVolumesItMakes(t *testing.T) {
	res := imp(t, "services:\n  a:\n    image: x\n    user: '1000:1000'\n    volumes:\n      - d:/d\nvolumes:\n  d:\n", nil)
	if !strings.Contains(res.YAML, "initial.uid: \"1000\"") || !strings.Contains(res.YAML, "oci.uid: \"1000\"") {
		t.Errorf("the user must own the volume and run the process:\n%s", res.YAML)
	}
}
