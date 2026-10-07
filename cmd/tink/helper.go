package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/incusapi"
)

func newHelperCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "helper",
		Short: "Look at the tink helper: the instance that runs tink's scheduled work next to the data",
		Long: `The helper is an Incus instance that runs "tink daemon run" next to the data: the backup scheduler and the ingress
reconcile. It publishes what it is doing on its own instance config, and these commands read that back. See docs/helper.md.`,
	}
	cmd.AddCommand(newHelperInstallCmd(), newHelperRemoveCmd(), newHelperStatusCmd())
	return cmd
}

func newHelperInstallCmd() *cobra.Command {
	var socket string
	var opts helper.InstallOptions
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Create the helper instance, start it, and enrol it with its own certificate",
		Long: `install creates the helper: a project of its own (tink-helper), an OCI app container in it that runs "tink daemon run", two volumes
(its client configuration, and its jobs), a NIC, and a proxy device that gives it the host's HTTPS API on its own loopback. It starts it,
and enrols it: the host is told to trust a certificate that the helper generates INSIDE the instance, so the private key never leaves it, using
a single-use token handed over on standard input (never on a command line, never on disk). The helper then publishes a status document, which
install waits for and reports.

It needs the host's API to be listening (core.https_address); the loopback address is enough, and tink deploy sets one.

With no flag, a release build installs the helper image published for its own version (ghcr:minihci/tink-helper:vX.Y.Z), the same binary built from the
same tag. A development build has no such image: give --image (an OCI image that has tink at /usr/local/bin/tink), or --binary FILE to put a linux tink
binary in a stock alpine image: the way to run a development build, or a helper where the image cannot be pulled. It is safe to run again: what exists is left alone,
and an enrolled helper is not enrolled twice. --reissue enrols it again with a fresh key pair (the old certificate is removed from the trust store).

The helper's certificate is revocable ("tink helper remove", or "incus config trust remove") and its requests are attributed to it. It is not
confined: it has the reach of root on the host, and nothing here claims otherwise.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Image == "" && opts.Binary == "" {
				// a release build knows the image published for it; a development build has none
				if opts.Image = helper.ReleaseImage(injectedVersion); opts.Image == "" {
					return fmt.Errorf("this is not a release build, so there is no published helper image that matches it: give --image (an OCI image with tink at /usr/local/bin/tink) or --binary FILE (a linux tink binary, run in a stock alpine image)")
				}
			}
			if opts.TZ == "" {
				opts.TZ = os.Getenv("TZ")
			}
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			out := cmd.OutOrStdout()
			if opts.TZ == "" {
				fmt.Fprintln(out, "note: no --timezone given (and no $TZ), so the helper evaluates schedules in UTC")
			}
			return (&helper.Installer{Server: server, Out: out}).Install(opts)
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&opts.Project, "project", helper.DefaultProject, "the project to put the helper in")
	cmd.Flags().StringVar(&opts.Name, "name", helper.DefaultName, "the helper instance's name")
	cmd.Flags().StringVar(&opts.Pool, "pool", "default", "the storage pool for its root disk and volumes")
	cmd.Flags().StringVar(&opts.Network, "network", "", "the network its NIC joins (default: the default profile's, else incusbr0)")
	cmd.Flags().StringVar(&opts.Image, "image", "", "the OCI image it runs (with tink at /usr/local/bin/tink)")
	cmd.Flags().StringVar(&opts.Binary, "binary", "", "a linux tink binary to put in the instance; with no --image, a stock alpine image is used")
	cmd.Flags().StringVar(&opts.TZ, "timezone", "", "the time zone schedules are evaluated in, e.g. America/Denver (default: $TZ, else UTC)")
	cmd.Flags().BoolVar(&opts.Reissue, "reissue", false, "enrol the helper again with a fresh key pair, removing its old certificate from the trust store")
	cmd.Flags().DurationVar(&opts.Wait, "wait", 90*time.Second, "how long to wait for the helper to report in (negative: do not wait)")
	return cmd
}

func newHelperRemoveCmd() *cobra.Command {
	var socket string
	var opts helper.RemoveOptions
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Revoke the helper's certificate, then stop and delete it",
		Long: `remove revokes the helper's certificate in the host's trust store, then stops and deletes the instance. Its two volumes (the job history, and its
client configuration with any keys for remote backup targets) stay, so installing again picks up where it was; --purge deletes them too, and the
project if that leaves it empty. Nothing is removed from the volumes' data: restore points on the backup targets are not the helper's to remove.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			return (&helper.Installer{Server: server, Out: cmd.OutOrStdout()}).Remove(opts)
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&opts.Project, "project", "", "the helper's project (default: wherever the helper is found)")
	cmd.Flags().StringVar(&opts.Name, "name", "", "the helper instance's name (default: the helper found)")
	cmd.Flags().BoolVar(&opts.Purge, "purge", false, "also delete the helper's volumes, and its project if that leaves it empty")
	return cmd
}

func newHelperStatusCmd() *cobra.Command {
	var socket, instance, project string
	var check, asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Say whether the helper is well, from what it publishes about itself",
		Long: `status finds the helper (an instance marked with user.tink.helper, in any project) and reads the status document it keeps on its
own instance config (user.tink.helper.status). That works when the helper has stopped, which is the case that matters: the last
thing it said is still there, going stale.

It judges the helper, from what it said and from the state of its instance:

  healthy   running, heard from recently, nothing skipped, nothing failing
  degraded  running and heard from, but skipping a volume, failing a copy, failing an ingress reconcile, or saying nothing useful
  down      stopped, not found, or silent for longer than 2.5 of its heartbeats

--check prints one line per helper and exits 0 for healthy, 1 for degraded and 2 for down, so cron, a monitor or Home Assistant can
poll it. Without --check it prints the details and exits 0 whatever it found, unless it could not look. Nothing here sends a
notification; it is what a notifier would run.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true, // execute prints an error once; the check modes say what they have to on standard output
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			return runHelperStatus(cmd.OutOrStdout(), server, instance, project, check, asJSON, time.Now())
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&instance, "instance", "", "look only at the helper instance with this name")
	cmd.Flags().StringVar(&project, "project", "", "look only at helpers in this project")
	cmd.Flags().BoolVar(&check, "check", false, "print one line per helper and exit 0 (healthy), 1 (degraded) or 2 (down)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print everything as JSON")
	return cmd
}

// runHelperStatus is the command with its connection and clock handed in, so it can be tested.
func runHelperStatus(out io.Writer, server incus.InstanceServer, instance, project string, check, asJSON bool, now time.Time) error {
	all, err := helper.Find(server)
	if err != nil {
		return err
	}
	var found []helper.Found
	for _, f := range all {
		if (instance == "" || f.Name == instance) && (project == "" || f.Project == project) {
			found = append(found, f)
		}
	}
	if len(found) == 0 {
		what := "no helper instance found (an instance with user.tink.helper set, in any project)"
		if instance != "" || project != "" {
			what = "no helper instance found matching the --instance and --project given"
		}
		if check {
			fmt.Fprintln(out, helper.Down.String()+": "+what)
			return &exitCodeError{code: int(helper.Down), silent: true}
		}
		return errors.New(what)
	}

	// Reading the trust store takes admin access; a client that cannot is simply not told, and the rest still holds.
	certs, certErr := server.GetCertificates()
	reports := make([]helper.Report, len(found))
	worst := helper.Healthy
	for i, f := range found {
		reports[i] = helper.Evaluate(f, now)
		if certErr == nil {
			reports[i].CheckTrust(certs)
		}
		if reports[i].Health > worst {
			worst = reports[i].Health
		}
	}

	switch {
	case asJSON:
		if err := printHelperJSON(out, reports); err != nil {
			return err
		}
	case check:
		for _, r := range reports {
			fmt.Fprintln(out, r.Summary())
		}
	default:
		for i, r := range reports {
			if i > 0 {
				fmt.Fprintln(out)
			}
			printHelperDetails(out, r, now)
		}
	}
	if check && worst != helper.Healthy {
		return &exitCodeError{code: int(worst), silent: true}
	}
	return nil
}

type helperJSON struct {
	Project string         `json:"project"`
	Name    string         `json:"name"`
	State   string         `json:"instance_state"`
	Health  string         `json:"health"`
	Code    int            `json:"exit_code"`
	Reasons []string       `json:"reasons,omitempty"`
	Status  *helper.Status `json:"status,omitempty"`
}

func printHelperJSON(out io.Writer, reports []helper.Report) error {
	list := make([]helperJSON, len(reports))
	for i, r := range reports {
		list[i] = helperJSON{Project: r.Found.Project, Name: r.Found.Name, State: r.Found.State, Health: r.Health.String(), Code: int(r.Health), Reasons: r.Reasons, Status: r.Status}
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", b)
	return nil
}

func printHelperDetails(out io.Writer, r helper.Report, now time.Time) {
	fmt.Fprintf(out, "helper %s: %s\n", r.Found.Label(), r.Health)
	for _, why := range r.Reasons {
		fmt.Fprintf(out, "  - %s\n", why)
	}
	fmt.Fprintf(out, "  instance:    %s\n", r.Found.State)
	st := r.Status
	if st == nil {
		return
	}
	fmt.Fprintf(out, "  version:     %s\n", orDash(st.Version))
	fmt.Fprintf(out, "  speaks:      jobs protocol %d; copy policies up to protocol %d; status protocol %d\n", st.JobProto, st.PolicyProto, st.Proto)
	fmt.Fprintf(out, "  time zone:   %s\n", orDash(st.TZ))
	if !st.Started.IsZero() {
		fmt.Fprintf(out, "  started:     %s (%s ago)\n", st.Started.Format("2006-01-02 15:04 MST"), roundAge(now.Sub(st.Started)))
	}
	if !st.Tick.IsZero() {
		fmt.Fprintf(out, "  last heard:  %s ago (it writes at least every %s)\n", roundAge(now.Sub(st.Tick)), roundAge(time.Duration(st.HeartbeatSeconds)*time.Second))
	}
	fmt.Fprintf(out, "  skipped:     %s\n", listOrNone(len(st.Skipped), func(i int) string { return fmt.Sprintf("%s: %s", st.Skipped[i].Volume, st.Skipped[i].Reason) }))
	fmt.Fprintf(out, "  failing:     %s\n", listOrNone(len(st.Failing), func(i int) string {
		f := st.Failing[i]
		return fmt.Sprintf("%s -> %s, %d in a row since %s", f.Volume, f.Target, f.Count, f.Since.Format("2006-01-02 15:04 MST"))
	}))
	if j := st.LastJob; j != nil {
		fmt.Fprintf(out, "  last job:    %s %s, %s ago\n", j.ID, j.State, roundAge(now.Sub(j.Finished)))
	} else {
		fmt.Fprintf(out, "  last job:    none finished\n")
	}
	switch i := st.Ingress; {
	case i == nil:
		fmt.Fprintf(out, "  ingress:     not run by this helper\n")
	case i.OK:
		fmt.Fprintf(out, "  ingress:     ok, %s ago\n", roundAge(now.Sub(i.At)))
	default:
		fmt.Fprintf(out, "  ingress:     the last reconcile FAILED, %s ago\n", roundAge(now.Sub(i.At)))
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func listOrNone(n int, item func(int) string) string {
	if n == 0 {
		return "none"
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = item(i)
	}
	return strings.Join(parts, "; ")
}

func roundAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
