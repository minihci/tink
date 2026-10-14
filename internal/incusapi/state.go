package incusapi

import (
	"fmt"
	"net/http"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// InstanceRunning says whether an instance has a live init process, which is what puts its disk devices in place over its root filesystem. Only
// then does the instance's own file API see what the instance sees; before that, a file pushed to a path under a mount lands in the root
// filesystem beneath it.
//
// The status string is not enough. Found live (2026-10-13, a crash-looping Authelia): Incus lists a container whose init has died as "Running",
// and its state call then fails with a 500, "Invalid PID -1" (the server asks for the init's start time and cannot find one). So, in order:
//
//   - a status other than Running (or Ready, which is Running plus the ready signal): not running, and why says the status;
//   - Running with a pid of 0 or less: not running, it has no init;
//   - the state call failing with that 500: not running, for the same reason;
//   - any other error: err, and running means nothing. A 404 is one of these: the caller has found the instance already, so it is not expected.
//
// why is a phrase for a sentence ("its status is Stopped") and is empty when the instance is running.
func InstanceRunning(s incus.InstanceServer, name string) (running bool, why string, err error) {
	st, _, err := s.GetInstanceState(name)
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusInternalServerError) && strings.Contains(err.Error(), "Invalid PID") {
			return false, "Incus lists it as Running but cannot find its init process: " + err.Error(), nil
		}
		return false, "", err
	}
	switch {
	case st.Status != "Running" && st.Status != "Ready":
		return false, "its status is " + st.Status, nil
	case st.Pid <= 0:
		return false, fmt.Sprintf("Incus lists it as %s but reports no init process (pid %d)", st.Status, st.Pid), nil
	}
	return true, "", nil
}
