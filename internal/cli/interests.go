package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
)

func newInterestsCmd() *cobra.Command {
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
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			res, err := ctx.Client.ListInterests(cmd.Context(), client.ListInterestsOpts{
				Limit:   limit,
				Members: members,
			})
			if err != nil {
				return err
			}
			renderInterests(res)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "Max interests to show")
	cmd.Flags().IntVar(&members, "members", 3, "Documents to preview per interest")
	cmd.AddCommand(newInterestsRebuildCmd())
	return cmd
}

func newInterestsRebuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Recompute interest clusters from the current corpus",
		Long: "Enqueue a clustering job that recomputes interests from all fetched,\n" +
			"indexed documents. Runs in the background; check progress with\n" +
			"`curio jobs --kind cluster` and view results with `curio interests`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			res, err := ctx.Client.RebuildInterests(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("clustering job enqueued: %s\n", res.JobID)
			fmt.Println("track it with `curio jobs --kind cluster`, then run `curio interests`")
			return nil
		},
	}
}

func renderInterests(res *client.InterestList) {
	if len(res.Items) == 0 {
		fmt.Println("no interests yet — run `curio interests rebuild` to compute them")
		fmt.Println("(clustering needs fetched + indexed documents to group)")
		return
	}

	fmt.Printf("%d interests across %d documents", len(res.Items), res.NumDocuments)
	if res.NumNoise > 0 {
		fmt.Printf(" (%d unclustered)", res.NumNoise)
	}
	if res.ComputedAt != nil {
		fmt.Printf(" — computed %s", res.ComputedAt.Local().Format("2006-01-02 15:04"))
	}
	fmt.Print("\n\n")

	for i, in := range res.Items {
		label := in.Label
		if label == "" {
			label = "(unlabeled)"
		}
		fmt.Printf("%2d. %s  —  %d docs (cohesion %.2f)\n", i+1, label, in.Size, in.Cohesion)
		if in.Summary != "" {
			for _, line := range wrapLines(in.Summary, 96) {
				fmt.Printf("    %s\n", line)
			}
		}
		for _, m := range in.Members {
			title := m.Title
			if title == "" {
				title = m.URL
			}
			fmt.Printf("      • %s\n", title)
			fmt.Printf("        doc_id: %s\n", m.DocID)
			if m.MarkdownPath != "" {
				fmt.Printf("        path:   %s\n", m.MarkdownPath)
			}
		}
		fmt.Println()
	}
}
