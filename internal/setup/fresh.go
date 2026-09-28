package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/samsar/curio/internal/curiohome"
)

// backupStamp is the time format of a --fresh backup's name.
const backupStamp = "20060102-150405"

// backupName is the name --fresh would move the home to, for the plan
// and the confirmation. The move names it again, the same unless a name
// was taken meanwhile, and reports what this can only show: a path it
// can't look at is shown as the first name it would try.
func (w *world) backupName(hs homeState) string {
	dir, err := resolveHome(hs.path)
	if err != nil {
		dir = hs.path
	}
	name, err := backupPath(dir, w.freshTime())
	if err != nil {
		return dir + ".bak-" + w.freshTime().Local().Format(backupStamp)
	}
	return name
}

// freshTime is the time the backup is named after, taken once.
func (w *world) freshTime() time.Time {
	if w.freshAt.IsZero() {
		w.freshAt = w.deps.Now()
	}
	return w.freshAt
}

// backupPath is the first free name for dir set aside at t:
// <dir>.bak-YYYYMMDD-HHMMSS in local time, then -2, -3 and so on. A name
// is taken by anything there, a dangling symlink or an empty directory
// included.
func backupPath(dir string, t time.Time) (string, error) {
	base := dir + ".bak-" + t.Local().Format(backupStamp)
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		_, err := os.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", fmt.Errorf("look at %s: %w", name, err)
		}
	}
}

// moveAside renames the directory the home resolves to, symlinks
// followed, to its backup name, with one rename, while no daemon runs for
// the home and none can start (daemonctl's WithDaemonStopped: the agent
// booted out, a daemon stopped, daemon.start.lock held throughout), then
// runs then on the emptied path under the same hold, so the new home
// exists before any daemon can start there. What isn't a curio home is
// never moved. Nothing in either tree is deleted; a rename that fails
// (another volume, a mount point, permissions) leaves the home where it
// was.
func (w *world) moveAside(ctx context.Context, hs homeState, then func(dir string) error) (string, error) {
	dir, err := resolveHome(hs.path)
	if err != nil {
		return "", err
	}
	env, err := w.connect(hs)
	if err != nil {
		return "", err
	}
	var dest string
	err = env.Controller.WithDaemonStopped(ctx, func() error {
		if _, err := curiohome.Open(dir); err != nil {
			return fmt.Errorf("not moving %s: %w", dir, err)
		}
		if dest, err = backupPath(dir, w.freshTime()); err != nil {
			return err
		}
		if err := os.Rename(dir, dest); err != nil {
			return fmt.Errorf("move %s aside: %w; it is left where it was", dir, err)
		}
		if err := then(dir); err != nil {
			return fmt.Errorf("moved %s aside to %s, then: %w", dir, dest, err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return dest, nil
}
