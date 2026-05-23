package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status, embedding info, and basic counts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}

			// Check daemon liveness without auto-starting; the user wants
			// to know if it's down.
			pctx, cancel := context.WithTimeout(cmd.Context(), 500*time.Millisecond)
			defer cancel()
			health, err := ctx.Client.Healthz(pctx)
			if err != nil {
				fmt.Println("daemon: not running")
				if ctx.Home != nil {
					fmt.Printf("home:   %s\n", ctx.Home.Path)
				}
				return nil
			}

			fmt.Printf("daemon:  running  (version %s)\n", health.Version)
			fmt.Printf("home:    %s\n", ctx.Home.Path)
			fmt.Printf("schema:  v%d\n", health.SchemaVersion)
			fmt.Printf("embed:   %s (dim %d)\n", health.EmbeddingModel, health.EmbeddingDim)
			return nil
		},
	}
}
