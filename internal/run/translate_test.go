package run

import (
	"reflect"
	"strings"
	"testing"
)

func TestPublishAcceptsWhatInstallLinesWrite(t *testing.T) {
	for in, want := range map[string]map[string]string{
		"8080:80":                   {"listen": "tcp:0.0.0.0:8080", "connect": "tcp:127.0.0.1:80"},
		"53:53/udp":                 {"listen": "udp:0.0.0.0:53", "connect": "udp:127.0.0.1:53"},
		"53:53/TCP":                 {"listen": "tcp:0.0.0.0:53", "connect": "tcp:127.0.0.1:53"},
		"127.0.0.1:8000:80":         {"listen": "tcp:127.0.0.1:8000", "connect": "tcp:127.0.0.1:80"},
		"192.168.1.5:7359:7359/udp": {"listen": "udp:192.168.1.5:7359", "connect": "udp:127.0.0.1:7359"},
		"[::1]:8000:80":             {"listen": "tcp:[::1]:8000", "connect": "tcp:127.0.0.1:80"},
		"8000-8010:8000-8010":       {"listen": "tcp:0.0.0.0:8000-8010", "connect": "tcp:127.0.0.1:8000-8010"},
		"9000-9002:80":              {"listen": "tcp:0.0.0.0:9000-9002", "connect": "tcp:127.0.0.1:80"},
	} {
		got, err := publishDevice(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		want["type"] = "proxy"
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %v, want %v", in, got, want)
		}
	}
}

func TestPublishRefusesWhatItCannotHonour(t *testing.T) {
	for in, want := range map[string]string{
		"80":                  "random host port",
		"http:80":             "host port",
		"8080:http":           "container port",
		"0:80":                "1-65535",
		"70000:80":            "1-65535",
		"8010-8000:80":        "backwards",
		"8000-8003:8000-8001": "must match",
		"80:80/sctp":          "tcp or udp",
		"localhost:80:80":     "not an IP address",
		"[::1:80:80":          "[ADDRESS]",
		"1.2.3.4:80:80:80":    "expected",
	} {
		_, err := publishDevice(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want one containing %q", in, err, want)
		}
	}
}

func TestVolumeOptionsMeanWhatDockerMeans(t *testing.T) {
	dev, notes, err := volumeDevice("/srv/music:/music:ro", "default")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"type": "disk", "source": "/srv/music", "path": "/music", "readonly": "true"}
	if !reflect.DeepEqual(dev, want) {
		t.Errorf("ro = %v, want %v", dev, want)
	}
	if len(notes) != 0 {
		t.Errorf("a read-only bind mount needs no warning about writing: %v", notes)
	}

	dev, _, _ = volumeDevice("/srv/music:/music:rw", "default")
	if _, there := dev["readonly"]; there {
		t.Errorf("rw is the default and says nothing: %v", dev)
	}

	// a managed volume can be read-only too
	dev, _, err = volumeDevice("seed:/seed:ro", "fast")
	if err != nil || dev["readonly"] != "true" || dev["pool"] != "fast" {
		t.Errorf("%v %v", dev, err)
	}
}

func TestShiftIsOptInAndSaidOutLoudWhenItIsNot(t *testing.T) {
	dev, notes, err := volumeDevice("/srv/media:/media:shift", "default")
	if err != nil || dev["shift"] != "true" || len(notes) != 0 {
		t.Errorf("shift: %v %v %v", dev, notes, err)
	}
	dev, _, _ = volumeDevice("/srv/media:/media:ro,shift", "default")
	if dev["shift"] != "true" || dev["readonly"] != "true" {
		t.Errorf("options combine: %v", dev)
	}
	// without ro or shift, the app will not be able to write, and the person is told which two ways out there are
	_, notes, _ = volumeDevice("/srv/media:/media", "default")
	if len(notes) != 1 || !strings.Contains(notes[0], ":ro") || !strings.Contains(notes[0], ":shift") || !strings.Contains(notes[0], "nobody") {
		t.Errorf("notes = %v", notes)
	}
	// a managed volume is mapped already and says nothing
	if _, notes, _ := volumeDevice("data:/data", "default"); len(notes) != 0 {
		t.Errorf("a managed volume: %v", notes)
	}
}

func TestVolumeRefusalsNameTheProblem(t *testing.T) {
	for in, want := range map[string]string{
		"./media:/media":       "relative path",
		"~/media:/media":       "relative path",
		"media/x:/media":       "relative path",
		"/srv:/media:ro,rw":    "ro and rw",
		"/srv:/media:zz":       "not supported",
		"/srv:/media:ro:extra": "expected SRC:DST",
		"/srv":                 "expected SRC:DST",
		":/media":              "source is empty",
		"/srv:media":           "must be absolute",
		"data:/data:shift":     "managed volume",
	} {
		_, _, err := volumeDevice(in, "default")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want one containing %q", in, err, want)
		}
	}
	// Docker options with no meaning here are accepted and the person is told they were dropped
	dev, notes, err := volumeDevice("/srv:/media:ro,z", "default")
	if err != nil || dev["readonly"] != "true" || len(notes) != 1 || !strings.Contains(notes[0], "ignored") {
		t.Errorf("%v %v %v", dev, notes, err)
	}
}

func TestMemoryIsTranslatedToIncusSuffixes(t *testing.T) {
	for in, want := range map[string]string{
		"512m": "512MiB", "512M": "512MiB", "512mb": "512MiB", "1g": "1GiB", "1G": "1GiB", "1.5g": "1536MiB",
		"2048m": "2GiB", "100k": "100KiB", "1073741824": "1GiB", "256 m": "256MiB",
	} {
		if got, err := parseMemory(in); err != nil || got != want {
			t.Errorf("%q = %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "lots", "0", "0m", "5x", "1000b", "-5m"} {
		if got, err := parseMemory(in); err == nil {
			t.Errorf("%q should be refused, got %q", in, got)
		}
	}
}

func TestCPUsIsDockersQuotaNotACount(t *testing.T) {
	for in, want := range map[string]string{"1": "100ms/100ms", "0.5": "50ms/100ms", "1.5": "150ms/100ms", "2": "200ms/100ms", "0.25": "25ms/100ms", "0.01": "1ms/100ms"} {
		if got, err := parseCPUs(in); err != nil || got != want {
			t.Errorf("%q = %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "two", "0.001", "NaN", "Inf"} {
		if got, err := parseCPUs(in); err == nil {
			t.Errorf("%q should be refused, got %q", in, got)
		}
	}
}

func TestDeviceMakesACharacterDeviceAndRefusesWhatItCannot(t *testing.T) {
	for in, want := range map[string]map[string]string{
		"/dev/ttyUSB0":                         {"source": "/dev/ttyUSB0", "path": "/dev/ttyUSB0"},
		"/dev/serial/by-id/usb-x:/dev/ttyACM0": {"source": "/dev/serial/by-id/usb-x", "path": "/dev/ttyACM0"},
		"/dev/kvm:/dev/kvm:rwm":                {"source": "/dev/kvm", "path": "/dev/kvm"},
		"/dev/net/tun::rw":                     {"source": "/dev/net/tun", "path": "/dev/net/tun"},
	} {
		got, err := deviceFlag(in)
		want["type"] = "unix-char"
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %v %v, want %v", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		"/dev/dri":            "gpu type=gpu",
		"/dev/dri/":           "gpu type=gpu",
		"/dev/sda":            "unix-block",
		"/dev/nvme0n1:/dev/x": "unix-block source=/dev/nvme0n1 path=/dev/x",
		"/tmp/file":           "under /dev/",
		"/dev/null:relative":  "absolute",
		"/dev/null:/dev/n:xy": "permissions",
		"/dev/a:/dev/b:r:z":   "expected HOST",
	} {
		if _, err := deviceFlag(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want one containing %q", in, err, want)
		}
	}
}

func TestBuildPutsTheTranslatorsTogether(t *testing.T) {
	spec, err := Build(Options{
		Name: "n", Image: "i", Pool: "default", Restart: "unless-stopped",
		Memory: "512m", CPUs: "1.5",
		Publish: []string{"53:53/tcp", "53:53/udp", "127.0.0.1:8000:80"},
		Volume:  []string{"/srv/music:/music:ro", "data:/data"},
		Device:  []string{"/dev/ttyUSB0:/dev/ttyACM0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"limits.memory": "512MiB", "limits.cpu.allowance": "150ms/100ms", "boot.autorestart": "true", "boot.autostart": "true",
	} {
		if spec.Config[k] != want {
			t.Errorf("%s = %q, want %q", k, spec.Config[k], want)
		}
	}
	if spec.Devices["proxy1"]["listen"] != "udp:0.0.0.0:53" || spec.Devices["proxy2"]["listen"] != "tcp:127.0.0.1:8000" {
		t.Errorf("devices = %v", spec.Devices)
	}
	if spec.Devices["device0"]["type"] != "unix-char" || spec.Devices["volume0"]["readonly"] != "true" {
		t.Errorf("devices = %v", spec.Devices)
	}
}

func TestRestartDecidesBothWhetherToRestartAndWhetherToComeBackAtBoot(t *testing.T) {
	for policy, want := range map[string]string{"always": "true", "unless-stopped": "true", "on-failure": "true", "no": "false"} {
		spec, err := Build(Options{Name: "n", Image: "i", Restart: policy})
		if err != nil || spec.Config["boot.autorestart"] != want || spec.Config["boot.autostart"] != want {
			t.Errorf("%s: %v %v", policy, spec.Config, err)
		}
	}
	// a flag's key is not the escape hatch's to override
	if _, err := Build(Options{Name: "n", Image: "i", Restart: "always", IncusConfig: []string{"boot.autostart=false"}}); err == nil || !strings.Contains(err.Error(), "--restart") {
		t.Errorf("err = %v", err)
	}
	// and with no --restart, nothing is said about either
	if spec, _ := Build(Options{Name: "n", Image: "i"}); spec.Config["boot.autostart"] != "" {
		t.Errorf("%v", spec.Config)
	}
}

func TestMemoryAndCPUsConflictWithTheEscapeHatch(t *testing.T) {
	for _, o := range []Options{
		{Memory: "1g", IncusConfig: []string{"limits.memory=2GiB"}},
		{CPUs: "1", IncusConfig: []string{"limits.cpu.allowance=50%"}},
	} {
		o.Name, o.Image = "n", "i"
		if _, err := Build(o); err == nil || !strings.Contains(err.Error(), "already set by --") {
			t.Errorf("%+v: err = %v", o, err)
		}
	}
}

func TestTheRecordedCommandCarriesMemoryCPUsAndDevices(t *testing.T) {
	got := CommandLine(Options{Name: "n", Image: "i", Memory: "512m", CPUs: "1.5", Device: []string{"/dev/ttyUSB0"}, Publish: []string{"53:53/udp"}})
	want := "tink run --name n --memory 512m --cpus 1.5 --device /dev/ttyUSB0 -p 53:53/udp i"
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}
