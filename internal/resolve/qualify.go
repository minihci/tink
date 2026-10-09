package resolve

import (
	"fmt"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/run"
)

// QualifyImages gives every instance's image: the same reading `tink run` gives an image argument (run.Qualify): a registry host written
// Docker's way becomes that host's OCI remote, a reference that names no registry is Docker Hub, and a host with no remote configured is an
// error that says how to add one. It rewrites resources in place and returns what the person should be told, one line each.
//
// It runs before the graph is built and only asks the live server whether a name is a local image already, so an image that the stack itself
// declares (a kind: image, by its name or its alias) is skipped: it does not exist yet, and is not a registry reference.
func QualifyImages(server incus.InstanceServer, resources []Resource) ([]string, error) {
	declared := map[string]bool{}
	for _, r := range resources {
		if r.Kind == KindImage {
			declared[r.Name] = true
			if r.Alias != "" {
				declared[r.Alias] = true
			}
		}
	}
	var lines []string
	for i, r := range resources {
		if r.Kind != KindInstance || r.Image == "" || declared[r.Image] {
			continue
		}
		q, err := run.Qualify(scopedServer(server, r), r.Image)
		if err != nil {
			return nil, fmt.Errorf("instance/%s: %w", r.Name, err)
		}
		// q.Note (an explicit registry host mapped to its remote) is not repeated on every plan: the person wrote it that way on purpose
		if q.Warning != "" {
			lines = append(lines, fmt.Sprintf("warning: instance/%s: %s", r.Name, q.Warning))
		}
		resources[i].Image = q.Image
	}
	return lines, nil
}
