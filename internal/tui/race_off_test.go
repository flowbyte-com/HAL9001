//go:build !race

package tui

// raceDetector reports whether the binary was built with the race
// detector.
//
// The detector slows every call and every allocation by a factor of
// several, so a time budget means nothing under it.
const raceDetector = false
