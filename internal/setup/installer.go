package setup

import (
	"context"
	"fmt"
)

// Detection is what Detect found installed of one package.
type Detection struct {
	// Brew is brew's path; empty without Homebrew.
	Brew string
	// Formula: Homebrew has the package's formula installed.
	Formula bool
	// App is the path of the package's macOS app, empty when none.
	App string
	// Binary is the package's command on PATH, empty when none. It may be
	// the formula's or the app's.
	Binary string
}

// Action is something an Installer does to a package.
type Action int

const (
	InstallFormula Action = iota // brew install <formula>
	StartService                 // brew services start <formula>
	UpgradeFormula               // brew upgrade <formula>
	RestartService               // brew services restart <formula>
	OpenApp                      // open -a <app>
	OpenURL                      // open <url>
)

// openPath is macOS's open(1).
const openPath = "/usr/bin/open"

// Command is the exact argv of action on target, a formula, an app's name
// or a URL, with brew as d found it.
func Command(d Detection, action Action, target string) []string {
	switch action {
	case InstallFormula:
		return []string{d.Brew, "install", target}
	case StartService:
		return []string{d.Brew, "services", "start", target}
	case UpgradeFormula:
		return []string{d.Brew, "upgrade", target}
	case RestartService:
		return []string{d.Brew, "services", "restart", target}
	case OpenApp:
		return []string{openPath, "-a", target}
	case OpenURL:
		return []string{openPath, target}
	default:
		panic(fmt.Sprintf("setup: unknown installer action %d", action))
	}
}

// Installer finds what is installed and runs the commands that install
// and start software: Homebrew first, the app from its maker's site
// otherwise. It never runs sudo.
type Installer interface {
	// Detect finds what is installed of a package, the Homebrew formula
	// formula and the macOS app app ("" for none), changing nothing.
	Detect(ctx context.Context, formula, app string) (Detection, error)
	// Run runs argv without a shell, its output streaming live through
	// ui's Output.
	Run(ctx context.Context, ui UI, argv []string) error
}

// cappedBuffer keeps the first max bytes written to it and drops the
// rest, never failing a write, so a chatty subprocess isn't blocked on its
// pipe.
type cappedBuffer struct {
	buf []byte
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.max - len(b.buf)
	b.buf = append(b.buf, p[:max(0, min(room, len(p)))]...)
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }
