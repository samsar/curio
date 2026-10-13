//go:build unix

package devtool

import (
	"fmt"
	"os"
	"syscall"
)

// hardLinks says how many hard links info's file has, "2 hard links", when
// it has more than one, and "" otherwise. Stat_t.Nlink is uint16 on darwin
// and uint64 or uint32 on linux, so it is compared and formatted as it is,
// never converted.
func hardLinks(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink <= 1 {
		return ""
	}
	return fmt.Sprintf("%d hard links", st.Nlink)
}
