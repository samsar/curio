// Package cli wires the curio CLI commands. The root command holds shared
// state (config, daemon controller, HTTP client) and the subcommands
// consume what they need from a *Context.
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/samansartipi/curio/internal/client"
	"github.com/samansartipi/curio/internal/config"
	"github.com/samansartipi/curio/internal/curiohome"
	"github.com/samansartipi/curio/internal/daemonctl"
)

// Context is the bag of state the subcommands read from.
type Context struct {
	Home       *curiohome.Home
	Config     config.Config
	Client     *client.Client
	Controller *daemonctl.Controller
}

// Execute builds the root command and runs it.
func Execute() error {
	root := newRootCmd()
	return root.Execute()
}

func newRootCmd() *cobra.Command {
	var (
		daemonURL string
		homeFlag  string
	)

	root := &cobra.Command{
		Use:   "curio",
		Short: "Personal context layer built from your bookmarks",
		Long: `curio imports your bookmarks, indexes the content, and exposes a hybrid
BM25 + vector search over the corpus. Talks to a long-running curio-daemon
over HTTP; auto-starts the daemon if it's not running.`,
		SilenceUsage:  true,
		SilenceErrors: false,
	}

	root.PersistentFlags().StringVar(&daemonURL, "daemon-url", "", "Override daemon base URL (default: http://127.0.0.1:8765)")
	root.PersistentFlags().StringVar(&homeFlag, "curio-home", "", "Override $CURIO_HOME (default: ~/.curio or $CURIO_HOME env)")

	// Persistent setup runs for every subcommand except those that opt out.
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		// `daemon` subcommands manage the daemon themselves; some of them
		// don't need an open Home. The individual commands check.
		ctx, err := buildContext(homeFlag, daemonURL)
		if err != nil {
			return err
		}
		cmd.SetContext(setCtx(cmd.Context(), ctx))
		return nil
	}

	root.AddCommand(
		newVersionCmd(),
		newAddCmd(),
		newImportCmd(),
		newSearchCmd(),
		newStatusCmd(),
		newDaemonCmd(),
	)
	return root
}

// buildContext resolves home, loads config, constructs client + controller.
// Tolerates a missing home for first-run UX — the daemon will init it.
func buildContext(homeFlag, daemonURL string) (*Context, error) {
	if homeFlag != "" {
		os.Setenv("CURIO_HOME", homeFlag)
	}
	homePath, err := curiohome.Resolve()
	if err != nil {
		return nil, err
	}

	// Auto-init on first run so users don't have to think about it. Use
	// the documented defaults (nomic-embed-text / 768); the daemon
	// re-checks against config on startup and fails loudly if it
	// disagrees.
	home, err := curiohome.Open(homePath)
	if errors.Is(err, curiohome.ErrNotInitialized) {
		home, err = curiohome.Init(homePath, "nomic-embed-text", 768)
		if err != nil {
			return nil, fmt.Errorf("initialize %s: %w", homePath, err)
		}
	} else if err != nil {
		return nil, err
	}

	cfg := config.Default()
	if home != nil {
		loaded, err := config.Load(home.ConfigPath())
		if err != nil {
			return nil, err
		}
		cfg = loaded
	}

	base := daemonURL
	if base == "" {
		base = "http://" + cfg.Daemon.Listen
	}

	daemonBin := os.Getenv("CURIO_DAEMON_BIN")
	if daemonBin == "" {
		// Default: look for curio-daemon next to the curio binary.
		exe, err := os.Executable()
		if err == nil {
			daemonBin = filepath.Join(filepath.Dir(exe), "curio-daemon")
		}
	}

	var ctrl *daemonctl.Controller
	if home != nil {
		ctrl = daemonctl.New(home, daemonBin, base)
	}

	return &Context{
		Home:       home,
		Config:     cfg,
		Client:     client.New(base),
		Controller: ctrl,
	}, nil
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, _ []string) {
			ctx, ok := getCtx(cmd.Context())
			if ok && ctx.Home != nil {
				if meta, err := ctx.Home.Meta(); err == nil {
					fmt.Printf("curio %s\nschema: v%d  embedder: %s/%d\n",
						versionString(), meta.SchemaVersion, meta.EmbeddingModel, meta.EmbeddingDim)
					return
				}
			}
			fmt.Printf("curio %s\n", versionString())
		},
	}
}
