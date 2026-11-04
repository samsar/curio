//go:build race

package ui

// raceSlowdown scales the render time bounds under -race, which runs
// goldmark's parse about seven times slower: the macOS runner took 11.7 s
// for a shape this machine renders in 0.5 s without it.
const raceSlowdown = 3
