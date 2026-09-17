package bootstrap

func applyDaemonConfig(r *runner, opts Options) error {
	rendered, err := renderServerConfig(
		opts.RepoRoot+"/daemon/server-config.yaml",
		opts.RepoRoot+"/daemon/authorization.star",
		opts.Config,
	)
	if err != nil {
		return err
	}
	// incus config edit replaces the server's ENTIRE config with whatever
	// is piped in -- deploy.sh's own server-config.yaml comment already
	// documents why that's safe here (this host's server config is
	// exclusively this repo's concern) and preserved as-is by this port.
	return r.runWithStdin("applied daemon server config (OIDC + authorization scriptlet)", rendered, "incus", "config", "edit")
}
