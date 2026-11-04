//go:build !race

package ui

// raceSlowdown scales the render time bounds under -race; 1 without it.
const raceSlowdown = 1
