package cli

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// resolveFilter turns a list command's filter flags into the one filter
// value it sends: the explicit --state/--status wins, then --failed, then
// --all (no filter), then the command's happy-path default
// (docs/decisions.md "CLI defaults: happy-path views; debug paths are
// opt-in").
func resolveFilter(explicit string, failed, all bool, happyPath string) string {
	switch {
	case explicit != "":
		return explicit
	case failed:
		return "failed"
	case all:
		return ""
	default:
		return happyPath
	}
}

// maxPageLimit is the largest page the daemon's list endpoints return.
const maxPageLimit = 500

// checkPageLimit refuses a --limit the daemon wouldn't honor: it answers a
// size outside 1..maxPageLimit with its default page instead.
func checkPageLimit(limit int) error {
	if limit < 1 || limit > maxPageLimit {
		return fmt.Errorf("--limit must be between 1 and %d, got %d", maxPageLimit, limit)
	}
	return nil
}

// printNextPage ends a list page that has a successor with the command that
// shows it: the command as it was run, flags and all, with --cursor set to
// next.
func printNextPage(w io.Writer, cmd *cobra.Command, next string) {
	if next == "" {
		return
	}
	args := []string{cmd.CommandPath()}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		switch {
		case f.Name == "cursor":
		case f.Value.Type() == "bool" && f.Value.String() == "true":
			args = append(args, "--"+f.Name)
		default:
			args = append(args, "--"+f.Name+"="+shellQuote(f.Value.String()))
		}
	})
	args = append(args, "--cursor="+shellQuote(next))
	fmt.Fprintf(w, "next page: %s\n", strings.Join(args, " "))
}

// shellSafe matches words a POSIX shell reads as themselves.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote quotes s for a POSIX shell, leaving plain words as they are.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
