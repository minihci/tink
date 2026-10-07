package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	cmd.AddCommand(newHelperStatusCmd())
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

	reports := make([]helper.Report, len(found))
	worst := helper.Healthy
	for i, f := range found {
		reports[i] = helper.Evaluate(f, now)
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
