package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
)

func newInterestsCmd(env *daemonctl.Env) *cobra.Command {
	var (
		limit   int
		members int
	)
	cmd := &cobra.Command{
		Use:   "interests",
		Short: "Show inferred topic clusters (interests) across your saved content",
		Long: "List the labeled topic clusters curio inferred from your library — a\n" +
			"picture of what you read about. Run `curio interests rebuild` to compute\n" +
			"or refresh them after adding content.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			res, err := env.Client.ListInterests(cmd.Context(), client.ListInterestsOpts{
				Limit:   limit,
				Members: members,
			})
			if err != nil {
				return err
			}
			renderInterests(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "Max interests to show")
	cmd.Flags().IntVar(&members, "members", 3, "Documents to preview per interest")
	cmd.AddCommand(newInterestsRebuildCmd(env))
	return cmd
}

func newInterestsRebuildCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Recompute interest clusters from the current corpus",
		Long: "Enqueue a clustering job that recomputes interests from all fetched,\n" +
			"indexed documents. Runs in the background; check progress with\n" +
			"`curio jobs --kind cluster` and view results with `curio interests`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			res, err := env.Client.RebuildInterests(cmd.Context())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "clustering job enqueued: %s\n", res.JobID)
			fmt.Fprintln(w, "track it with `curio jobs --kind cluster`, then run `curio interests`")
			return nil
		},
	}
}

func renderInterests(w io.Writer, res *client.InterestList) {
	if len(res.Items) == 0 {
		fmt.Fprintln(w, "no interests yet — run `curio interests rebuild` to compute them")
		fmt.Fprintln(w, "(clustering needs fetched + indexed documents to group)")
		return
	}

	fmt.Fprintf(w, "%d interests across %d documents", len(res.Items), res.NumDocuments)
	if res.NumNoise > 0 {
		fmt.Fprintf(w, " (%d unclustered)", res.NumNoise)
	}
	if res.ComputedAt != nil {
		fmt.Fprintf(w, " — computed %s", res.ComputedAt.Local().Format("2006-01-02 15:04"))
	}
	fmt.Fprint(w, "\n\n")

	for i, in := range res.Items {
		label := in.Label
		if label == "" {
			label = "(unlabeled)"
		}
		fmt.Fprintf(w, "%2d. %s  —  %d docs (cohesion %.2f)\n", i+1, label, in.Size, in.Cohesion)
		if in.Summary != "" {
			for _, line := range wrapLines(in.Summary, 96) {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
		for _, m := range in.Members {
			title := m.Title
			if title == "" {
				title = m.URL
			}
			fmt.Fprintf(w, "      • %s\n", title)
			fmt.Fprintf(w, "        doc_id: %s\n", m.DocID)
			if m.MarkdownPath != "" {
				fmt.Fprintf(w, "        path:   %s\n", m.MarkdownPath)
			}
		}
		fmt.Fprintln(w)
	}
}
