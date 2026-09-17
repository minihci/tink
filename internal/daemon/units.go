package daemon

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"
)

// UnitOptions parameterizes the generated unit -- deliberately small,
// just the two things that actually vary between hosts.
type UnitOptions struct {
	ExecPath string   // path to the tink binary on the target host
	Args     []string // arguments to pass, e.g. ["daemon", "run", "--interval=60s"]
}

// DefaultUnitOptions matches how "tink daemon run" is meant to actually
// run -- one binary, no separate tinkd (see the daemon package doc for
// why this isn't a client/server split either).
func DefaultUnitOptions() UnitOptions {
	return UnitOptions{
		ExecPath: "/usr/local/bin/tink",
		Args:     []string{"daemon", "run"},
	}
}

func (o UnitOptions) commandLine() string {
	parts := append([]string{o.ExecPath}, o.Args...)
	return strings.Join(parts, " ")
}

var systemdTemplate = template.Must(template.New("systemd").Parse(
	`[Unit]
Description=Tink ingress reconciler daemon
After=network.target

[Service]
Type=simple
ExecStart={{.CommandLine}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`))

var openrcTemplate = template.Must(template.New("openrc").Parse(
	`#!/sbin/openrc-run

name="tink-daemon"
description="Tink ingress reconciler daemon"
command="{{.ExecPath}}"
command_args="{{.CommandArgs}}"
command_background="yes"
pidfile="/run/${RC_SVCNAME}.pid"

depend() {
	need net
}
`))

// SystemdUnit renders a systemd .service file.
func SystemdUnit(opts UnitOptions) (string, error) {
	var buf bytes.Buffer
	data := struct{ CommandLine string }{opts.commandLine()}
	if err := systemdTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering systemd unit: %w", err)
	}
	return buf.String(), nil
}

// OpenRCScript renders an OpenRC init.d script.
func OpenRCScript(opts UnitOptions) (string, error) {
	var buf bytes.Buffer
	data := struct {
		ExecPath    string
		CommandArgs string
	}{opts.ExecPath, strings.Join(opts.Args, " ")}
	if err := openrcTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering OpenRC script: %w", err)
	}
	return buf.String(), nil
}

// InitSystems lists the recognized values for the --init flag.
var InitSystems = []string{"systemd", "openrc"}

// Generate renders the unit/script for the named init system.
func Generate(initSystem string, opts UnitOptions) (string, error) {
	switch initSystem {
	case "systemd":
		return SystemdUnit(opts)
	case "openrc":
		return OpenRCScript(opts)
	default:
		return "", fmt.Errorf("unrecognized init system %q (want one of: %s)", initSystem, strings.Join(InitSystems, ", "))
	}
}

// DetectInit guesses the running init system from well-known filesystem
// markers -- /run/systemd/system only exists when systemd is actually
// PID 1 (not just installed), and openrc-run is OpenRC's own init
// script interpreter. Returns "" if neither is found, rather than
// guessing wrong.
func DetectInit() string {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return "systemd"
	}
	if _, err := os.Stat("/sbin/openrc-run"); err == nil {
		return "openrc"
	}
	return ""
}
