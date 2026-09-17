package bootstrap

import (
	"fmt"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/minihci/tink/internal/incusapi"
)

type profileSpec struct {
	name         string
	templatePath string // relative to RepoRoot
}

var profileSpecs = []profileSpec{
	{name: "incus-ui", templatePath: "incus-ui/incus-ui.profile.yaml"},
	{name: "authelia", templatePath: "authelia/authelia.profile.yaml"},
	{name: "ingress", templatePath: "ingress/ingress.profile.yaml"},
}

// applyProfiles creates each profile if it doesn't exist yet, then
// unconditionally pushes the freshly rendered content -- matching
// deploy.sh exactly, which re-applies every profile on every run rather
// than diffing first. Uses the real Incus Go client (CreateProfile /
// UpdateProfile), not `incus profile create|edit` shelled out -- these
// are clean, verified, single-purpose API calls, unlike instance launch
// (see instances.go for why that one still shells out).
func applyProfiles(r *runner, opts Options) error {
	server, err := incusapi.Connect(opts.socket())
	if err != nil {
		return fmt.Errorf("connecting to incus: %w", err)
	}

	for _, spec := range profileSpecs {
		if err := applyOneProfile(r, server, spec, opts); err != nil {
			return err
		}
	}
	return nil
}

func applyOneProfile(r *runner, server incus.InstanceServer, spec profileSpec, opts Options) error {
	rendered, err := parseProfileYAML(opts.RepoRoot+"/"+spec.templatePath, opts.Config)
	if err != nil {
		return err
	}

	_, etag, err := server.GetProfile(spec.name)
	notFound := err != nil

	if r.dryRun {
		if notFound {
			r.note("would create profile %s", spec.name)
		}
		r.note("would apply profile %s", spec.name)
		return nil
	}

	if notFound {
		if err := server.CreateProfile(api.ProfilesPost{
			Name:       spec.name,
			ProfilePut: rendered,
		}); err != nil {
			return fmt.Errorf("creating profile %s: %w", spec.name, err)
		}
		r.note("created profile %s", spec.name)
		// A freshly created profile has its own ETag; re-fetch rather
		// than assume CreateProfile already applied the full content
		// (it does, but UpdateProfile below still needs a real ETag).
		_, etag, err = server.GetProfile(spec.name)
		if err != nil {
			return fmt.Errorf("re-reading just-created profile %s: %w", spec.name, err)
		}
	} else {
		r.note("profile %s already exists", spec.name)
	}

	if err := server.UpdateProfile(spec.name, rendered, etag); err != nil {
		return fmt.Errorf("updating profile %s: %w", spec.name, err)
	}
	r.note("applied profile %s", spec.name)
	return nil
}
