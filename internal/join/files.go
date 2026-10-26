package join

import (
	"fmt"
	"io"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// maxFile bounds what is read from an instance: a hosts file or a zone file is small, and anything larger is not what the join expects.
const maxFile = 1 << 20

// IncusFiles is the join's file API on a real server: the instance file API of whichever project the file is in. `tink join apply` and the daemon's
// reconciler both write through it.
type IncusFiles struct{ Server incus.InstanceServer }

func (f IncusFiles) Read(project, instance, path string) (string, bool, error) {
	rc, _, found, err := incusapi.LookupInstanceFile(f.Server.UseProject(project), instance, path)
	if err != nil || !found {
		return "", false, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxFile+1))
	if err != nil {
		return "", false, err
	}
	if len(b) > maxFile {
		return "", false, fmt.Errorf("%s is larger than %d bytes, which is not a file the join expects", path, maxFile)
	}
	return string(b), true, nil
}

func (f IncusFiles) Write(project, instance, path, content string) error {
	return f.Server.UseProject(project).CreateInstanceFile(instance, path, incus.InstanceFileArgs{
		Content:   strings.NewReader(content),
		Type:      "file",
		WriteMode: "overwrite",
		Mode:      0o644,
	})
}
