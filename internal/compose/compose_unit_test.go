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
	res := imp(t, "services:\n  a:\n    image: x\n    build: .\n    security_opt: [no-new-privileges:true]\n  b:\n    image: y\n    network_mode: host\n", nil)
	got := refusals(res)
	for _, want := range []string{"service a: build:", "service a: security_opt:", "service b: network_mode:"} {
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
		// a default is itself expanded, as compose does; $NAME is a variable without braces
		{"$SET", "v1"},
		{"${UNSET:-$SET}", "v1"},
		{"${UNSET:-${SET}/x}", "v1/x"},
		{"${UNSET:-${ALSO_UNSET:-deep}}", "deep"},
		{"${SET}-$SET", "v1-v1"},
	}
	for _, c := range cases {
		res := imp(t, "services:\n  a:\n    image: x\n    environment:\n      K: '"+c.in+"'\n", files)
		want := "environment.K: " + scalar(c.want)
		if !strings.Contains(res.YAML, want) {
			t.Errorf("%s: want %q in:\n%s\n%s", c.in, want, res.YAML, refusals(res))
		}
	}
	for _, bad := range []string{"${UNSET}", "$UNSET", "${UNSET:-$ALSO_UNSET}", "${SET:?needed}", "$", "a$ b", "${SET"} {
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

// The three keys the survey of Home Assistant's companions needed (#205), read the way tink run reads the same flags.
func TestPrivilegedDevicesAndTmpfs(t *testing.T) {
	res := imp(t, `services:
  a:
    image: x
    privileged: true
    shm_size: 512mb
    devices:
      - /dev/ttyUSB0:/dev/ttyACM0
    tmpfs:
      - /run:size=64m,noexec
    volumes:
      - type: tmpfs
        target: /tmp/cache
        tmpfs:
          size: 1000000000
`, nil)
	if len(res.Refused) > 0 {
		t.Fatal(refusals(res))
	}
	for _, want := range []string{
		`security.privileged: "true"`,
		"type: unix-char", "source: /dev/ttyUSB0", "path: /dev/ttyACM0",
		"path: /dev/shm", "size: 512MiB",
		"path: /run", "size: 64MiB",
		"path: /tmp/cache", "size: \"1000000000\"",
	} {
		if !strings.Contains(res.YAML, want) {
			t.Errorf("missing %q in:\n%s", want, res.YAML)
		}
	}
	if strings.Contains(res.YAML, "shm_size was dropped") {
		t.Error("shm_size is translated, not dropped")
	}
	notes := strings.Join(res.Notes, "\n")
	for _, want := range []string{"--privileged becomes security.privileged", `"noexec" at /run is ignored`} {
		if !strings.Contains(notes, want) {
			t.Errorf("missing note %q in:\n%s", want, notes)
		}
	}
}

func TestPrivilegedFalseAndStringsFromVariables(t *testing.T) {
	res := imp(t, "services:\n  a:\n    image: x\n    privileged: ${P:-false}\n", nil)
	if len(res.Refused) > 0 || strings.Contains(res.YAML, "security.privileged") {
		t.Errorf("false must change nothing:\n%s\n%s", refusals(res), res.YAML)
	}
	if got := refusals(imp(t, "services:\n  a:\n    image: x\n    privileged: maybe\n", nil)); !strings.Contains(got, "privileged") {
		t.Errorf("a value that is not a boolean must be refused, got %q", got)
	}
}

func TestDevicesRefusals(t *testing.T) {
	for dev, why := range map[string]string{"/dev/sda:/dev/sda": "block device", "/dev/dri:/dev/dri": "directory", "/tmp/x:/x": "under /dev/"} {
		if got := refusals(imp(t, "services:\n  a:\n    image: x\n    devices:\n      - "+dev+"\n", nil)); !strings.Contains(got, "service a: devices:") || !strings.Contains(got, why) {
			t.Errorf("%s: want a devices refusal containing %q, got %q", dev, why, got)
		}
	}
	if got := refusals(imp(t, "services:\n  a:\n    image: x\n    devices:\n      - source: /dev/x\n        target: /dev/x\n", nil)); !strings.Contains(got, "long form") {
		t.Errorf("the long form must be refused, got %q", got)
	}
}

func TestTmpfsRefusals(t *testing.T) {
	for _, c := range []struct{ svc, why string }{
		{"tmpfs: /x\n    shm_size: 1g\n    volumes:\n      - type: tmpfs\n        target: /x\n", "two tmpfs mounts at /x"},
		{"tmpfs: [relative]\n", "absolute"},
		{"volumes:\n      - type: tmpfs\n        target: /x\n        tmpfs: {size: big}\n", "expected a size"},
		{"volumes:\n      - type: tmpfs\n        target: /x\n        tmpfs: {nope: 1}\n", "not in the supported subset"},
	} {
		if got := refusals(imp(t, "services:\n  a:\n    image: x\n    "+c.svc, nil)); !strings.Contains(got, c.why) {
			t.Errorf("%q: want a refusal containing %q, got %q", c.svc, c.why, got)
		}
	}
}

// The refusals that name a way out: host networking, and the two hardening keys Incus has no key for.
func TestRefusalsSayWhatToDo(t *testing.T) {
	for _, c := range []struct{ svc, key, why string }{
		{"network_mode: host", "network_mode", "a NIC on the LAN's bridge"},
		{"network_mode: bridge", "network_mode", `"bridge" is not supported`},
		{"security_opt: [apparmor:unconfined]", "security_opt", "raw.apparmor"},
		{"read_only: true", "read_only", "did not start"},
		{"cap_drop: [ALL]", "cap_drop", "fixed set of capabilities"},
	} {
		got := refusals(imp(t, "services:\n  a:\n    image: x\n    "+c.svc+"\n", nil))
		if !strings.Contains(got, "service a: "+c.key+":") || !strings.Contains(got, c.why) {
			t.Errorf("%s: want a %s refusal containing %q, got %q", c.svc, c.key, c.why, got)
		}
	}
}
