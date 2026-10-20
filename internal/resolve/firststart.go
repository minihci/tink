package resolve

// Placing an instance's files before its first start. See docs/first-start-files-design.md.
//
// A service that needs one of its files at start (Headscale needs /etc/headscale.yaml) used to be created, started, and only then given the file by
// the kind: file resource that targets it: the app exited at once, Incus restarted it in a loop, and tink's own calls that fell in the gaps failed.
// An instance that was created and never started takes a file push (found live), so createInstance now places the instance's eligible files after
// creating it and before starting it. The file resources then find their content already in place and plan as "no changes" (applyOne plans each
// resource right before acting on it): no second push, and no restart for a file with restart: true.
//
// One exception: a file at or below the mount path of a disk device would be pushed into the rootfs underneath the mount and hidden once the device is
// mounted at the start. Those files go into the volume, or wait for the start; see firststartmount.go.

import (
	"errors"
	"strconv"
	"strings"

	"github.com/minihci/tink/internal/incusapi"
)

// instanceKey identifies an instance by project and name, since two projects may each have one of the same name.
func instanceKey(project, name string) string { return project + "/" + name }

// firstStartFiles chooses, for each container instance a stack declares, the kind: file resources to place before it first starts, in declaration
// order. A file is chosen when it targets such an instance and everything it waits for (depends_on) is that instance or another chosen file of it.
// A file that waits for anything else, an exec for one, is left to its own turn: it may need something that has not happened yet. A virtual machine
// has no entry: it needs its agent running to take a file.
//
// These are the candidates. Whether a candidate can be pushed into the stopped instance at all depends on the instance's disk devices, which only
// creating (or planning) it can see in full, so placeBy (firststartmount.go) takes it from here: a file a mount would hide is written into the volume
// or left for after the start.
//
// The rule is worked out to a fixed point, since leaving out one file makes any file that waits for it ineligible too. resources must already have
// their projects inherited (Levels does that), so a file and its instance agree on a project.
func firstStartFiles(resources []Resource) map[string][]Resource {
	containers := map[string]Resource{}
	for _, r := range resources {
		if r.Kind == KindInstance && !r.VM {
			containers[instanceKey(r.Project, r.Name)] = r
		}
	}
	candidates := map[string][]Resource{}
	for _, r := range resources {
		if r.Kind != KindFile {
			continue
		}
		k := instanceKey(r.Project, r.Instance)
		if _, ok := containers[k]; ok {
			candidates[k] = append(candidates[k], r)
		}
	}

	out := map[string][]Resource{}
	for k, keep := range candidates {
		inst := containers[k]
		for changed := true; changed; {
			changed = false
			ok := map[string]bool{inst.Name: true}
			for _, f := range keep {
				ok[f.Name] = true
			}
			var next []Resource
			for _, f := range keep {
				eligible := true
				for _, d := range f.DependsOn {
					if !ok[d] {
						eligible = false
						break
					}
				}
				if eligible {
					next = append(next, f)
				} else {
					changed = true
				}
			}
			keep = next
		}
		if len(keep) > 0 {
			out[k] = keep
		}
	}
	return out
}

// instanceSteps are the four things creating an instance does, so the order they happen in can be tested without a server.
type instanceSteps struct {
	create func() error
	config func() error
	place  func(f Resource) error
	start  func() error
}

// createInOrder creates the instance, applies its config, places each file, and starts it, in that order, and returns the names of the files that
// were placed. Placing is an ordering improvement, never a new way to fail: a push that fails is skipped (and not named), the instance is still
// started, and the file resource fails in its own turn with the message it has always had. A failed create or config stops everything, and places
// nothing.
//
// A step that is abandoned at its deadline (incusapi.ErrTimeout) is not a failure of that kind: it may yet be done. It stops everything after it, a
// push included (a push the server never answered is not a push that failed), and the instance is not started on top of it.
func createInOrder(s instanceSteps, files []Resource) (placed []string, err error) {
	if err := s.create(); err != nil {
		return nil, err
	}
	if err := s.config(); err != nil {
		return nil, err
	}
	for _, f := range files {
		err := s.place(f)
		if errors.Is(err, incusapi.ErrTimeout) {
			return placed, err
		}
		if err == nil {
			placed = append(placed, f.Name)
		}
	}
	return placed, s.start()
}

// placedNote is what an apply says about the files placed with their instance.
func placedNote(instance string, placed []string) string {
	names := make([]string, len(placed))
	for i, n := range placed {
		names[i] = "file/" + n
	}
	return "instance/" + instance + ": placed " + strconv.Itoa(len(placed)) + " file(s) before its first start: " + strings.Join(names, ", ")
}
