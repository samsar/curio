package cli

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
