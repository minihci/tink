package incusapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// stateServer answers GetInstanceState with a table entry. Any other method panics (nil embedded interface).
type stateServer struct {
	incus.InstanceServer
	state *api.InstanceState
	err   error
	asked []string
}

func (s *stateServer) GetInstanceState(name string) (*api.InstanceState, string, error) {
	s.asked = append(s.asked, name)
	return s.state, "", s.err
}

func TestInstanceRunning(t *testing.T) {
	cases := []struct {
		name        string
		state       *api.InstanceState
		err         error
		wantRunning bool
		wantWhy     string // a substring
		wantErr     bool
	}{
		{"running with an init", &api.InstanceState{Status: "Running", Pid: 7281}, nil, true, "", false},
		{"ready (running, and it said so) with an init", &api.InstanceState{Status: "Ready", Pid: 7281}, nil, true, "", false},
		{"stopped", &api.InstanceState{Status: "Stopped"}, nil, false, "its status is Stopped", false},
		{"frozen", &api.InstanceState{Status: "Frozen", Pid: 7281}, nil, false, "its status is Frozen", false},
		{"in error", &api.InstanceState{Status: "Error"}, nil, false, "its status is Error", false},
		{"starting, with a pid already", &api.InstanceState{Status: "Starting", Pid: 7281}, nil, false, "its status is Starting", false},
		{"running with pid -1 in the document", &api.InstanceState{Status: "Running", Pid: -1}, nil, false, "reports no init process (pid -1)", false},
		{"running with pid 0 in the document", &api.InstanceState{Status: "Running", Pid: 0}, nil, false, "reports no init process (pid 0)", false},
		{"what the server really does for a Running container with a dead init: a 500 from the state call itself",
			nil, api.StatusErrorf(http.StatusInternalServerError, "Invalid PID -1"), false, "cannot find its init process: Invalid PID -1", false},
		{"the same, wrapped by something on the way", nil, errors.Join(errors.New("asking"), api.StatusErrorf(http.StatusInternalServerError, "Invalid PID 0")), false, "Invalid PID 0", false},
		{"a 500 that is about something else is an error, not a guess", nil, api.StatusErrorf(http.StatusInternalServerError, "database is locked"), false, "", true},
		{"a 403 is an error", nil, api.StatusErrorf(http.StatusForbidden, "not authorized"), false, "", true},
		{"the words Invalid PID in a 403 are still an error: only the server's own 500 counts", nil, api.StatusErrorf(http.StatusForbidden, "Invalid PID"), false, "", true},
		{"a 404 is an error: the caller found the instance already", nil, api.StatusErrorf(http.StatusNotFound, "Instance not found"), false, "", true},
		{"a dropped connection is an error", nil, errors.New("read tcp: connection reset by peer"), false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &stateServer{state: tc.state, err: tc.err}
			running, why, err := InstanceRunning(s, "sso")
			if running != tc.wantRunning || (err != nil) != tc.wantErr {
				t.Fatalf("running=%v err=%v, want running=%v err=%v", running, err, tc.wantRunning, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, tc.err) {
				t.Errorf("the original error must come back, got %v", err)
			}
			if !strings.Contains(why, tc.wantWhy) || (tc.wantWhy == "" && why != "") {
				t.Errorf("why = %q, want it to contain %q", why, tc.wantWhy)
			}
			if len(s.asked) != 1 || s.asked[0] != "sso" {
				t.Errorf("asked %v", s.asked)
			}
		})
	}
}
