//go:build !unix

package devtool

import "os"

// hardLinks says "" for every file: this platform's os.FileInfo carries no
// link count, and the developer's tools run on darwin and linux.
func hardLinks(os.FileInfo) string { return "" }
