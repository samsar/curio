package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/samansartipi/curio/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:   "curio",
		Short: "Personal context layer built from your bookmarks",
		Long: `Curio imports your browser bookmarks, fetches and indexes the content,
and provides hybrid BM25 + vector search over the corpus. See https://github.com/samansartipi/curio for details.`,
	}

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("curio", version.String())
		},
	})

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
