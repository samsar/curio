package setup

import (
	"regexp"
	"strconv"
)

// gpuCoreCount is the line of `ioreg -rc AGXAccelerator -d1` that counts
// an Apple GPU's cores: `"gpu-core-count" = 40`.
var gpuCoreCount = regexp.MustCompile(`"gpu-core-count"\s*=\s*(\d+)`)

// parseGPUCores reads the GPU core count from ioreg's output: 0 when it
// has none, as an Intel Mac's or a virtual machine's doesn't.
func parseGPUCores(out string) int {
	m := gpuCoreCount.FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0 // a count too large for an int is no count
	}
	return n
}
