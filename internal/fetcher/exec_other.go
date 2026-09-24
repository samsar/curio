//go:build !unix

package fetcher

import "os/exec"

// isolateProcessGroup leaves cmd as it is: without Unix process groups,
// cancellation kills only the tool itself, and WaitDelay bounds how long a
// helper it spawned can hold the output pipes.
func isolateProcessGroup(*exec.Cmd) {}
