package resolve

import (
	"strings"
	"testing"
)

func TestDiffDevicesNamesTheFieldThatDiffers(t *testing.T) {
	cur := map[string]map[string]string{"proxy0": {"type": "proxy", "listen": "tcp:0.0.0.0:3101", "connect": "tcp:127.0.0.1:3001"}}
	want := map[string]map[string]string{
		"proxy0": {"type": "proxy", "listen": "tcp:0.0.0.0:3201", "connect": "tcp:127.0.0.1:3001"},
		"eth0":   {"type": "nic", "network": "incusbr0"},
	}
	got := strings.Join(diffDevices(cur, want), "\n")
	for _, w := range []string{
		`device.proxy0.listen: "tcp:0.0.0.0:3101" -> "tcp:0.0.0.0:3201"`,
		`device.eth0: added (type=nic network=incusbr0)`,
	} {
		if !strings.Contains(got, w) {
			t.Errorf("diff = %q, want a line %q", got, w)
		}
	}
	if strings.Contains(got, "connect") {
		t.Errorf("an unchanged field is not a change: %q", got)
	}
	if d := diffDevices(cur, map[string]map[string]string{"proxy0": cur["proxy0"]}); len(d) != 0 {
		t.Errorf("identical devices differ: %v", d)
	}
	// a field the live device has and the stack no longer says is reported as removed
	got = strings.Join(diffDevices(map[string]map[string]string{"d": {"type": "disk", "readonly": "true"}}, map[string]map[string]string{"d": {"type": "disk"}}), "\n")
	if !strings.Contains(got, `device.d.readonly: "true" -> (unset)`) {
		t.Errorf("diff = %q", got)
	}
}
