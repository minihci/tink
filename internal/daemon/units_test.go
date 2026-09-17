package daemon

import (
	"strings"
	"testing"
)

func TestSystemdUnit_ContainsCommandLine(t *testing.T) {
	out, err := SystemdUnit(UnitOptions{ExecPath: "/usr/local/bin/tink", Args: []string{"daemon", "run", "--interval=30s"}})
	if err != nil {
		t.Fatalf("SystemdUnit returned error: %v", err)
	}
	if !strings.Contains(out, "ExecStart=/usr/local/bin/tink daemon run --interval=30s") {
		t.Fatalf("expected ExecStart line with full command, got:\n%s", out)
	}
	if !strings.Contains(out, "Restart=on-failure") {
		t.Fatalf("expected a restart policy, got:\n%s", out)
	}
}

func TestOpenRCScript_ContainsCommandAndArgs(t *testing.T) {
	out, err := OpenRCScript(UnitOptions{ExecPath: "/usr/local/bin/tink", Args: []string{"daemon", "run", "--interval=30s"}})
	if err != nil {
		t.Fatalf("OpenRCScript returned error: %v", err)
	}
	if !strings.Contains(out, `command="/usr/local/bin/tink"`) {
		t.Fatalf("expected command= line, got:\n%s", out)
	}
	if !strings.Contains(out, `command_args="daemon run --interval=30s"`) {
		t.Fatalf("expected command_args= line, got:\n%s", out)
	}
}

func TestGenerate_DispatchesAndRejectsUnknown(t *testing.T) {
	opts := DefaultUnitOptions()

	if _, err := Generate("systemd", opts); err != nil {
		t.Errorf("Generate(systemd) returned error: %v", err)
	}
	if _, err := Generate("openrc", opts); err != nil {
		t.Errorf("Generate(openrc) returned error: %v", err)
	}
	if _, err := Generate("sysvinit", opts); err == nil {
		t.Error("expected an error for an unrecognized init system, got nil")
	}
}
