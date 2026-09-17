package bootstrap

import "fmt"

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
// than diffing first.
func applyProfiles(r *runner, opts Options) error {
	existing, err := incusListNames("profile")
	if err != nil {
		return err
	}

	for _, spec := range profileSpecs {
		if !existing[spec.name] {
			if _, err := r.run(fmt.Sprintf("created profile %s", spec.name), "incus", "profile", "create", spec.name); err != nil {
				return err
			}
		} else {
			r.note("profile %s already exists", spec.name)
		}

		rendered, err := render(opts.RepoRoot+"/"+spec.templatePath, opts.Config)
		if err != nil {
			return err
		}
		if err := r.runWithStdin(fmt.Sprintf("applied profile %s", spec.name), rendered, "incus", "profile", "edit", spec.name); err != nil {
			return err
		}
	}
	return nil
}
