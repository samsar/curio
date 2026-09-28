package setup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/importer"
)

// withheld is a source macOS withholds: it can be named, and read never.
type withheld string

func (s withheld) Name() string { return string(s) }
func (withheld) Label() string  { return importer.LabelSafari }
func (s withheld) Spec() string { return strings.ToLower(string(s)) }
func (withheld) Check(context.Context) importer.Availability {
	return importer.Availability{State: importer.NeedsPermission, Reason: "operation not permitted"}
}
func (withheld) Parse(context.Context) ([]importer.ParsedBookmark, error) {
	return nil, importer.ErrEmpty
}

// refusingInstaller fails the test on any command it is asked to run.
type refusingInstaller struct{ t *testing.T }

func (i refusingInstaller) Detect(context.Context, string, string) (Detection, error) {
	i.t.Error("detected an install")
	return Detection{}, errors.New("no installs here")
}

func (i refusingInstaller) Run(_ context.Context, _ UI, argv []string) error {
	i.t.Errorf("ran %v", argv)
	return errors.New("no commands here")
}

// TestAllowed_NobodyToAsk: Apply's guard, for a source withheld only after
// the plan found it readable: in a run that can't walk the user through
// Full Disk Access (--yes here) it fails at once with the remedy, asking
// and opening nothing.
func TestAllowed_NobodyToAsk(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	var out bytes.Buffer
	ui := yesUI{newLineUI(strings.NewReader(""), &out)}
	w := &world{opts: Options{Yes: true}, deps: Deps{UI: ui, Installer: refusingInstaller{t}}}
	err := w.allowed(t.Context(), ui, withheld("Safari"))
	require.EqualError(t, err, "Safari: macOS doesn't let curio read it without Full Disk Access, which only you "+
		"can turn on, so --yes can't answer for it (give Terminal Full Disk Access in System Settings > Privacy & "+
		"Security > Full Disk Access, quit and reopen it, then run `curio up --import safari` again)")
	assert.Empty(t, out.String(), "nothing said, asked or run")
}
