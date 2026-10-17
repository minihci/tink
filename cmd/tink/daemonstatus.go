package main

import (
	"errors"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/incusapi"
)

// statusTarget is where `tink daemon run` publishes its status document: a custom volume's config (the helper's default, because a
// volume has no devices, so the right to edit one is not a route to root) or, for a helper installed before there was a status volume,
// the config of an instance. Neither means the daemon publishes nothing.
type statusTarget struct {
	Instance string // --status-instance
	Volume   string // --status-volume
	Pool     string // --status-pool
	Project  string // --status-project: of the instance, or of the volume
	// PoolGiven says --status-pool was given, so that it can be refused without --status-volume (its default is not "given").
	PoolGiven bool
}

// validate refuses the combinations that would publish in two places, or name a pool for no volume.
func (t statusTarget) validate() error {
	if t.Instance != "" && t.Volume != "" {
		return errors.New("--status-instance and --status-volume are two places for the one status document: give one (--status-volume, unless this helper was installed before the status volume existed)")
	}
	if t.PoolGiven && t.Volume == "" {
		return errors.New("--status-pool is the pool of --status-volume, and is meaningless without it")
	}
	return nil
}

// publisher is the publisher for the target, or nil when there is none. The volume form writes the one key and carries every other key
// on the volume through (incusapi.MergeVolumeConfig), and does not write the helper marker: that is on the instance, put there by
// `helper install`, and the helper holds no right to write an instance. Each write opens its own connection (connect), so a daemon that
// outlives a restart of Incus publishes again without being restarted.
func (t statusTarget) publisher(connect func() (incus.InstanceServer, error), heartbeat time.Duration) *helper.Publisher {
	switch {
	case t.Volume != "":
		return &helper.Publisher{
			Heartbeat: heartbeat,
			NoMarker:  true,
			Patch: func(config map[string]string) error {
				server, err := connect()
				if err != nil {
					return err
				}
				return incusapi.MergeVolumeConfig(server, t.Project, t.Pool, t.Volume, config)
			},
		}
	case t.Instance != "":
		return &helper.Publisher{
			Heartbeat: heartbeat,
			Patch: func(config map[string]string) error {
				server, err := connect()
				if err != nil {
					return err
				}
				return incusapi.PatchInstanceConfig(server, t.Project, t.Instance, config)
			},
		}
	}
	return nil
}

// connectorFor is what opens a connection to Incus on socket (or the remote chosen for this process), as every other command does.
func connectorFor(socket string) func() (incus.InstanceServer, error) {
	return func() (incus.InstanceServer, error) { return incusapi.Connect(socket) }
}
