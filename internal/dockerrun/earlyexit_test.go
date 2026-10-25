package dockerrun

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/minihci/tink/internal/instance"
)

// consoleFake is a server whose instance "app" has the status status (or is not there, or cannot be read), and whose console log is log (or cannot be read).
type consoleFake struct {
	incus.InstanceServer
	status   string
	gone     bool
	instErr  error
	log      string
	logErr   error
	logReads int
}

func (f *consoleFake) GetInstance(name string) (*api.Instance, string, error) {
	switch {
	case f.instErr != nil:
		return nil, "", f.instErr
	case f.gone:
		return nil, "", errNotFound
	}
	return &api.Instance{Name: name, Status: f.status}, "", nil
}

func (f *consoleFake) GetInstanceConsoleLog(string, *incus.InstanceConsoleLogArgs) (io.ReadCloser, error) {
	f.logReads++
	if f.logErr != nil {
		return nil, f.logErr
	}
	return io.NopCloser(strings.NewReader(f.log)), nil
}

func collect() (func(string, ...any), *[]string) {
	var lines []string
	return func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }, &lines
}

func appSpec() *instance.Spec { return &instance.Spec{Name: "app", Config: map[string]string{}} }

func TestCheckStillRunning(t *testing.T) {
	t.Run("running: nothing is said and the console is not read", func(t *testing.T) {
		f := &consoleFake{status: "Running"}
		note, lines := collect()
		if err := checkStillRunning(f, appSpec(), 0, note); err != nil || len(*lines) != 0 || f.logReads != 0 {
			t.Errorf("err %v, lines %q, console reads %d", err, *lines, f.logReads)
		}
	})

	t.Run("stopped: the end of the console log is printed and the run fails", func(t *testing.T) {
		var b strings.Builder
		for i := 1; i <= 30; i++ {
			fmt.Fprintf(&b, "line %d\r\n", i)
		}
		b.WriteString("\n\n")
		f := &consoleFake{status: "Stopped", log: b.String()}
		note, lines := collect()
		err := checkStillRunning(f, appSpec(), 0, note)
		if err == nil || !strings.Contains(err.Error(), "app stopped straight after it started") {
			t.Fatalf("err = %v", err)
		}
		if len(*lines) != 21 || !strings.HasPrefix((*lines)[0], "app stopped straight after it started (status Stopped); the last lines of its console log:") {
			t.Fatalf("lines = %q", *lines)
		}
		if (*lines)[1] != "  line 11" || (*lines)[20] != "  line 30" {
			t.Errorf("the tail is %q ... %q, want lines 11 to 30", (*lines)[1], (*lines)[20])
		}
	})

	t.Run("stopped with an empty console log: it says so", func(t *testing.T) {
		f := &consoleFake{status: "Stopped"}
		note, lines := collect()
		if err := checkStillRunning(f, appSpec(), 0, note); err == nil {
			t.Fatal("no error")
		}
		if len(*lines) != 1 || !strings.Contains((*lines)[0], "its console log is empty") {
			t.Errorf("lines = %q", *lines)
		}
	})

	t.Run("stopped with no console log: it says so", func(t *testing.T) {
		f := &consoleFake{status: "Stopped", logErr: errNotFound}
		note, lines := collect()
		_ = checkStillRunning(f, appSpec(), 0, note)
		if len(*lines) != 1 || !strings.Contains((*lines)[0], "no console log to show") {
			t.Errorf("lines = %q", *lines)
		}
	})

	t.Run("stopped and the console log cannot be read: the cause is printed, not hidden", func(t *testing.T) {
		f := &consoleFake{status: "Stopped", logErr: errors.New("connection reset")}
		note, lines := collect()
		err := checkStillRunning(f, appSpec(), 0, note)
		if err == nil || len(*lines) != 1 || !strings.Contains((*lines)[0], "could not be read: connection reset") {
			t.Errorf("err %v, lines %q", err, *lines)
		}
	})

	t.Run("gone: an ephemeral instance says it was deleted with its log", func(t *testing.T) {
		spec := appSpec()
		spec.Ephemeral = true
		err := checkStillRunning(&consoleFake{gone: true}, spec, 0, func(string, ...any) {})
		if err == nil || !strings.Contains(err.Error(), "ephemeral") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("gone, not ephemeral: it is an error that says so", func(t *testing.T) {
		err := checkStillRunning(&consoleFake{gone: true}, appSpec(), 0, func(string, ...any) {})
		if err == nil || !strings.Contains(err.Error(), "is gone straight after it started") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("the instance cannot be read: that is the error, and it is not read as 'running'", func(t *testing.T) {
		err := checkStillRunning(&consoleFake{instErr: api.StatusErrorf(500, "boom")}, appSpec(), 0, func(string, ...any) {})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("the settle time is waited", func(t *testing.T) {
		start := time.Now()
		if err := checkStillRunning(&consoleFake{status: "Running"}, appSpec(), 30*time.Millisecond, func(string, ...any) {}); err != nil {
			t.Fatal(err)
		}
		if time.Since(start) < 30*time.Millisecond {
			t.Errorf("returned after %v", time.Since(start))
		}
	})
}

func TestExplainConsole_AfterAFailedStart(t *testing.T) {
	f := &consoleFake{log: "mkdir: can't create directory '/var/lib/grafana/plugins': Permission denied\n"}
	note, lines := collect()
	explainConsole(f, appSpec(), note, "app did not start")
	want := []string{"app did not start; the last lines of its console log:", "  mkdir: can't create directory '/var/lib/grafana/plugins': Permission denied"}
	if fmt.Sprint(*lines) != fmt.Sprint(want) {
		t.Errorf("lines = %q, want %q", *lines, want)
	}
}

// What the app prints can include a value passed with -e: the ones whose names look like secrets are redacted, as they are in the recorded command line.
func TestConsoleTail_RedactsTheValuesOfSecretLookingVariables(t *testing.T) {
	spec := appSpec()
	spec.Config["environment.DB_PASSWORD"] = "hunter2-hunter2"
	spec.Config["environment.TZ"] = "Etc/UTC-Etc/UTC"
	f := &consoleFake{status: "Stopped", log: "connecting with hunter2-hunter2\nTZ is Etc/UTC-Etc/UTC\n"}
	note, lines := collect()
	_ = checkStillRunning(f, spec, 0, note)
	out := strings.Join(*lines, "\n")
	if strings.Contains(out, "hunter2-hunter2") {
		t.Errorf("the secret-looking value was printed:\n%s", out)
	}
	if !strings.Contains(out, "TZ is Etc/UTC-Etc/UTC") {
		t.Errorf("a value of an ordinary variable was changed:\n%s", out)
	}
}

func TestDefaultOptionsSettle(t *testing.T) {
	if got := DefaultOptions().Settle; got != DefaultSettle || got <= 0 {
		t.Errorf("Settle = %v", got)
	}
}
