package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
)

func newPauseCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "pause",
		Short: "Stop starting jobs; running ones finish, and the pause survives restarts",
		Long: `Pause the queue: the daemon starts no new fetch, index or clustering job
until 'curio resume'. Jobs already running finish. The pause is stored, so
it holds across daemon restarts. Starts the daemon if it isn't running.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return updateQueue(cmd, env, client.QueueUpdate{Paused: new(true)})
		},
	}
}

func newResumeCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Start jobs again after 'curio pause'; a schedule still applies",
		Long: `Resume the queue after 'curio pause'. A schedule still applies: outside
its window the queue stays closed until the window opens ('curio schedule
off' drops it).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return updateQueue(cmd, env, client.QueueUpdate{Paused: new(false)})
		},
	}
}

func newThrottleCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "throttle gentle|normal",
		Short: "Run fewer jobs at once to spare the machine (gentle), or all of them (normal)",
		Long: `Set how hard the daemon works through its queue. gentle runs fewer
fetches and index jobs at once, which keeps Ollama, and the machine, cooler
during a large import; normal runs every worker the daemon has. The setting
is stored and takes effect as running jobs finish.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateQueue(cmd, env, client.QueueUpdate{Throttle: args[0]})
		},
	}
}

func newScheduleCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "schedule HH:MM-HH:MM|off",
		Short: "Start jobs only in a daily window on the daemon's clock, or at any time (off)",
		Long: `Let the daemon start jobs only between two times of day, on its own
clock: 22:00-07:00 runs overnight. Jobs running when the window closes
finish. off drops the window, so the queue runs now. A pause still applies.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateQueue(cmd, env, client.QueueUpdate{Schedule: args[0]})
		},
	}
}

func newKeepAwakeCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "keep-awake on|off",
		Short: "Keep the Mac from idle sleep while jobs are queued and it runs on AC power (on), or let it sleep (off)",
		Long: `With keep-awake on, the daemon holds the Mac out of idle sleep (caffeinate -i)
while its workers have jobs queued or running, the queue isn't paused, and
the Mac runs on AC power, so an import carries on unattended; it lets go
when the queue drains, on battery, and when paused. A schedule keeps the
hold while it waits for its window. A closed lid still sleeps a laptop.
The setting is stored and takes effect at once. 'curio status' shows
whether the Mac is being held awake. Starts the daemon if it isn't running.`,
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{keepAwakeOn, keepAwakeOff},
		RunE: func(cmd *cobra.Command, args []string) error {
			on := args[0] == keepAwakeOn
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			q, err := env.Client.UpdateQueue(cmd.Context(), client.QueueUpdate{KeepAwake: &on})
			if err != nil {
				return queueError(err)
			}
			if q.KeepAwake {
				fmt.Fprintln(cmd.OutOrStdout(), "keep-awake: on (the daemon keeps the Mac from idle sleep "+
					"while jobs are queued and it runs on AC power)")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "keep-awake: off")
			}
			return nil
		},
	}
}

// keep-awake's arguments.
const (
	keepAwakeOn  = "on"
	keepAwakeOff = "off"
)

// updateQueue makes sure the daemon runs, applies u, and prints the queue
// as it is afterwards. The daemon validates u.
func updateQueue(cmd *cobra.Command, env *daemonctl.Env, u client.QueueUpdate) error {
	if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
		return err
	}
	q, err := env.Client.UpdateQueue(cmd.Context(), u)
	if err != nil {
		return queueError(err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "queue: "+describeQueue(q))
	return nil
}

// queueError explains a daemon that answers 404 for /v1/queue: one started
// before the queue controls existed, still running after an upgrade. The
// route has no IDs that could be missing, so a 404 can only mean that.
func queueError(err error) error {
	if client.IsNotFound(err) {
		return fmt.Errorf("the running curio-daemon has no queue controls: it predates them. "+
			"Restart it: run `curio daemon stop`, and the next command starts the installed one (%w)", err)
	}
	return err
}

// describeQueue says whether the queue is open and why not, with the
// settings that shape it, in one line.
func describeQueue(q *client.Queue) string {
	switch {
	case q.State == client.QueueOpen:
		return withSettings(q, "open", true)
	case q.State == client.QueueClosed && q.Reason == client.ReasonPaused:
		return fmt.Sprintf("%s; %s, nothing new starts (curio resume)",
			withSettings(q, "paused", true), finishing(running(q)))
	case q.State == client.QueueClosed && q.Reason == client.ReasonOutsideSchedule:
		return fmt.Sprintf("%s; opens %s (curio schedule off runs it now)",
			withSettings(q, "closed outside schedule "+q.Schedule, false), opensAt(q))
	case q.Reason != "":
		return fmt.Sprintf("%s (%s)", q.State, q.Reason)
	default:
		return q.State
	}
}

// whyClosed says why a closed queue starts nothing and what opens it, e.g.
// "paused (curio resume)"; empty when q is open or unknown (nil).
func whyClosed(q *client.Queue) string {
	switch {
	case q == nil || q.State != client.QueueClosed:
		return ""
	case q.Reason == client.ReasonPaused:
		return "paused (curio resume)"
	case q.Reason == client.ReasonOutsideSchedule:
		return fmt.Sprintf("closed outside schedule %s until %s (curio schedule off)", q.Schedule, opensAt(q))
	default:
		return fmt.Sprintf("closed (%s)", q.Reason)
	}
}

// opensAt is when a queue closed outside its schedule opens, on the local
// clock, as HH:MM.
func opensAt(q *client.Queue) string { return q.OpensAt.Local().Format("15:04") }

// withSettings follows state with the throttle, when it isn't normal, and,
// when schedule is set, the schedule, when there is one.
func withSettings(q *client.Queue, state string, schedule bool) string {
	parts := []string{state}
	if q.Throttle != client.ThrottleNormal {
		parts = append(parts, fmt.Sprintf("throttled %s (%s at once)", q.Throttle, throttledLimits(q)))
	}
	if schedule && q.Schedule != "" {
		parts = append(parts, "schedule "+q.Schedule)
	}
	return strings.Join(parts, ", ")
}

// throttledLimits lists the limits of the kinds a throttle caps, e.g.
// "fetch 4, index 1". Clustering runs one job at a time whatever the
// throttle, so its limit says nothing.
func throttledLimits(q *client.Queue) string {
	var parts []string
	for _, k := range q.Kinds {
		if k.Kind != "cluster" {
			parts = append(parts, fmt.Sprintf("%s %d", k.Kind, k.Limit))
		}
	}
	return strings.Join(parts, ", ")
}

func running(q *client.Queue) int {
	n := 0
	for _, k := range q.Kinds {
		n += k.Running
	}
	return n
}

// dueLater is how many of q's pending jobs can't run yet (a retry's
// backoff, a deferral's hold), and when the first of them can; 0 and zero
// when q is nil, none waits for a time, or the daemon doesn't say.
func dueLater(q *client.Queue) (n int, next time.Time) {
	if q == nil {
		return 0, time.Time{}
	}
	for _, k := range q.Kinds {
		n += k.DueLater
		if !k.NextDue.IsZero() && (next.IsZero() || k.NextDue.Before(next)) {
			next = k.NextDue
		}
	}
	return n, next
}

// finishing says how many running jobs finish.
func finishing(n int) string {
	switch n {
	case 0:
		return "no jobs running"
	case 1:
		return "1 running job finishes"
	default:
		return fmt.Sprintf("%d running jobs finish", n)
	}
}

// queueStatusTimeout bounds status's queue read, as its counts are.
const queueStatusTimeout = time.Second

// printQueue prints status's queue lines: the queue's state and each
// pool's load, with how many of its pending jobs are due later.
func printQueue(ctx context.Context, w io.Writer, c *client.Client) {
	ctx, cancel := context.WithTimeout(ctx, queueStatusTimeout)
	defer cancel()
	q, err := c.Queue(ctx)
	if err != nil {
		fmt.Fprintf(w, "queue:     unavailable: %v\n", queueError(err))
		return
	}
	fmt.Fprintf(w, "queue:     %s\n", describeQueue(q))
	loads := make([]string, 0, len(q.Kinds))
	for _, k := range q.Kinds {
		load := fmt.Sprintf("%s %d/%d running, %d pending", k.Kind, k.Running, k.Limit, k.Pending)
		if k.DueLater > 0 {
			load += fmt.Sprintf(" (%d due later)", k.DueLater)
		}
		loads = append(loads, load)
	}
	if len(loads) > 0 {
		fmt.Fprintf(w, "           %s\n", strings.Join(loads, "   "))
	}
	fmt.Fprintf(w, "keep-awake: %s\n", describeKeepAwake(q))
}

// describeKeepAwake says whether keep-awake is on and, if it is, whether
// the Mac is held awake and, if not, why not, in the keeper's order.
func describeKeepAwake(q *client.Queue) string {
	queued := 0
	for _, k := range q.Kinds {
		queued += k.Pending + k.Running
	}
	switch {
	case !q.KeepAwake:
		return "off"
	case q.KeepAwakeActive:
		return fmt.Sprintf("on, holding the Mac awake (AC power, %s queued)", plural(queued, "job"))
	case q.Paused:
		return "on, not holding: the queue is paused"
	case queued == 0:
		return "on, not holding: nothing queued"
	case q.PowerSource == client.PowerBattery:
		return "on, not holding: on battery power"
	case q.PowerSource != client.PowerAC:
		return "on, not holding: power source unknown"
	default:
		return "on, not holding yet"
	}
}

// plural is n and noun, "s" added unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
