package helper

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/jobs"
)

// JobsDir is the helper's jobs directory, inside its instance (on the data volume).
const JobsDir = dataMount + "/jobs"

// maxJobFile bounds what is read back from the helper in one piece. A job's log is limited to a megabyte by the executor, so this is
// slack, not a limit anyone should meet.
const maxJobFile = 8 << 20

// InstanceFS is a directory inside an instance, reached through the instance file API, as a jobs.FS. It is how a client on another
// machine does what the daemon does with the directory itself, and it is not atomic: a reader can see a file while it is being
// written, which is why the jobs protocol writes READY last. Every call is a request to the host and leaves an event in its log, so
// callers read as little as they can.
type InstanceFS struct {
	// Server is scoped to the instance's project.
	Server   incus.InstanceServer
	Instance string
	Root     string
}

var _ jobs.FS = InstanceFS{}

func (f InstanceFS) path(name string) string { return path.Join(f.Root, name) }

func missing(op, name string, err error) error {
	if incusapi.IsNotFound(err) {
		return &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
	}
	return fmt.Errorf("%s %s: %w", op, name, err)
}

func (f InstanceFS) ReadFile(name string) ([]byte, error) {
	rc, resp, err := f.Server.GetInstanceFile(f.Instance, f.path(name))
	if err != nil {
		return nil, missing("read", name, err)
	}
	if rc == nil || (resp != nil && resp.Type == "directory") {
		return nil, fmt.Errorf("read %s: it is a directory", name)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxJobFile+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(b) > maxJobFile {
		return nil, fmt.Errorf("read %s: larger than %d bytes", name, maxJobFile)
	}
	return b, nil
}

func (f InstanceFS) WriteFile(name string, data []byte) error {
	err := f.Server.CreateInstanceFile(f.Instance, f.path(name), incus.InstanceFileArgs{
		Content: bytes.NewReader(data), Type: "file", WriteMode: "overwrite", Mode: 0o600,
	})
	if err != nil {
		return missing("write", name, err)
	}
	return nil
}

func (f InstanceFS) Mkdir(name string) error {
	err := f.Server.CreateInstanceFile(f.Instance, f.path(name), incus.InstanceFileArgs{Type: "directory", Mode: 0o700})
	if err != nil {
		return missing("mkdir", name, err)
	}
	return nil
}

func (f InstanceFS) Remove(name string) error {
	if err := f.Server.DeleteInstanceFile(f.Instance, f.path(name)); err != nil && !incusapi.IsNotFound(err) {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

func (f InstanceFS) Exists(name string) (bool, error) {
	rc, _, found, err := incusapi.LookupInstanceFile(f.Server, f.Instance, f.path(name))
	if err != nil {
		return false, fmt.Errorf("look for %s: %w", name, err)
	}
	if rc != nil {
		rc.Close()
	}
	return found, nil
}

func (f InstanceFS) ReadDir(name string) ([]string, error) {
	rc, resp, err := f.Server.GetInstanceFile(f.Instance, f.path(name))
	if err != nil {
		return nil, missing("readdir", name, err)
	}
	if rc != nil {
		rc.Close()
	}
	if resp == nil || resp.Type != "directory" {
		return nil, fmt.Errorf("readdir %s: not a directory", name)
	}
	return resp.Entries, nil
}

// Pick chooses the helper a command is about: the only one, or the one named. None, or several that the flags do not narrow to one,
// is an error that says what to do.
func Pick(all []Found, instance, project string) (Found, error) {
	var match []Found
	for _, f := range all {
		if (instance == "" || f.Name == instance) && (project == "" || f.Project == project) {
			match = append(match, f)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		if instance != "" || project != "" {
			return Found{}, fmt.Errorf("no helper matches the --instance and --project given")
		}
		return Found{}, fmt.Errorf("no helper found on this server (an instance with %s set, in any project); `tink helper install` adds one", MarkerKey)
	default:
		labels := make([]string, len(match))
		for i, m := range match {
			labels[i] = m.Label()
		}
		return Found{}, fmt.Errorf("more than one helper (%s): choose with --instance and --project", strings.Join(labels, ", "))
	}
}

// JobsStore is the helper's jobs directory, reached from this machine. The instance has to be running: the file API reads a running
// instance, and a stopped helper is doing nothing with its jobs in any case.
func JobsStore(server incus.InstanceServer, f Found) (jobs.Store, error) {
	if f.State != "Running" {
		return jobs.Store{}, fmt.Errorf("the helper %s is %s: its jobs are on its volume and can be read only while it runs (`incus start %s --project %s`)",
			f.Label(), strings.ToLower(f.State), f.Name, f.Project)
	}
	return jobs.Store{FS: InstanceFS{Server: server.UseProject(f.Project), Instance: f.Name, Root: JobsDir}}, nil
}
