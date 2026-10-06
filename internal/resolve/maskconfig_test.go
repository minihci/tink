package resolve

import (
	"strings"
	"testing"
)

func TestDiffConfigHidesBothSidesOfASensitiveKey(t *testing.T) {
	const old, now = "old-plain-pw-1234", "new-plain-pw-5678"
	changes := diffConfig(
		map[string]string{"environment.DB_PASSWORD": old, "limits.memory": "1GiB"},
		map[string]string{"environment.DB_PASSWORD": now, "limits.memory": "2GiB"}, nil)
	joined := strings.Join(changes, "\n")
	for _, leak := range []string{old, now} {
		if strings.Contains(joined, leak) {
			t.Fatalf("a changed password printed %q:\n%s", leak, joined)
		}
	}
	// the old value matters most: swapping a plain password for a ${secret:} reference must not
	// print the plain one that is being replaced.
	if !strings.Contains(joined, "config.environment.DB_PASSWORD: (value hidden) changed") {
		t.Errorf("expected a hidden-value line, got:\n%s", joined)
	}
	if !strings.Contains(joined, `config.limits.memory: "1GiB" -> "2GiB"`) {
		t.Errorf("ordinary keys must still show their values, got:\n%s", joined)
	}
}

func TestDiffConfigHidesNewSensitiveKeyAndProvenanceKeys(t *testing.T) {
	changes := diffConfig(map[string]string{}, map[string]string{
		"environment.API_TOKEN": "tok-abcdef-123456",
		"environment.INNOCENT":  "from-a-secret-ref-value",
	}, map[string]bool{"environment.INNOCENT": true})
	joined := strings.Join(changes, "\n")
	if strings.Contains(joined, "tok-abcdef") || strings.Contains(joined, "from-a-secret-ref") {
		t.Fatalf("leaked:\n%s", joined)
	}
	if !strings.Contains(joined, "environment.API_TOKEN: (value hidden) set") ||
		!strings.Contains(joined, "environment.INNOCENT: (value hidden) set") {
		t.Errorf("a new key is 'set', and a key known to hold a secret is hidden whatever it is called:\n%s", joined)
	}
}

func TestRuntimeConfigDiffDoesNotPrintSensitiveValues(t *testing.T) {
	// an instance whose live password differs from the default the new image bakes in: the report names
	// the key but must show neither value. Ordinary keys keep showing theirs.
	diffs, keys := runtimeConfigDiff(
		map[string]string{"environment.DB_PASSWORD": "live-password-1234", "environment.TZ": "UTC"},
		map[string]string{},
		ociRuntime{User: "0", Env: []string{"DB_PASSWORD=image-default-5678", "TZ=Etc/UTC"}})
	joined := strings.Join(diffs, "\n")
	for _, leak := range []string{"live-password-1234", "image-default-5678"} {
		if strings.Contains(joined, leak) {
			t.Fatalf("the image-drift report printed %q:\n%s", leak, joined)
		}
	}
	if !strings.Contains(joined, "environment.DB_PASSWORD: instance and new image differ: (value hidden)") {
		t.Errorf("the sensitive key should still be reported, hidden:\n%s", joined)
	}
	if !strings.Contains(joined, `environment.TZ: instance has "UTC", new image wants "Etc/UTC"`) {
		t.Errorf("an ordinary key should still show its values:\n%s", joined)
	}
	if len(keys) != 2 {
		t.Errorf("both keys are still reported as stale, got %v", keys)
	}
}

func TestChangesEnvironment(t *testing.T) {
	if !changesEnvironment([]string{`config.limits.memory: "1" -> "2"`, "config.environment.X: (value hidden) changed"}) {
		t.Error("an environment change must be detected")
	}
	if changesEnvironment([]string{`config.limits.memory: "1" -> "2"`}) {
		t.Error("a non-environment change must not be")
	}
}
