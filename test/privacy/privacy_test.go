// Package privacy_test keeps the public repository free of the two personal
// identifiers that are easy to reintroduce: a personal mailbox and a real
// home directory. It is deliberately narrow; the real rule is in CLAUDE.md:
// no real library data, in docs, tests or fixtures.
package privacy_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var homePath = regexp.MustCompile(`/Users/[A-Za-z0-9._-]+`)

// syntheticHomes are placeholder home directories used in docs and tests.
var syntheticHomes = map[string]bool{
	"/Users/me":  true,
	"/Users/x":   true,
	"/Users/you": true,
}

func TestNoPersonalIdentifiers(t *testing.T) {
	root := filepath.Join("..", "..")
	var bad []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".md", ".go", ".yaml", ".yml", ".txt", ".json", ".html", ".plist":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		if strings.Contains(s, "@gmail"+".com") {
			bad = append(bad, path+": personal mailbox")
		}
		for _, home := range homePath.FindAllString(s, -1) {
			if !syntheticHomes[home] {
				bad = append(bad, path+": real home path "+home)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("personal identifiers in the public tree:\n%s", strings.Join(bad, "\n"))
	}
}
