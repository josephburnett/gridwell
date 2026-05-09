// Package anim is the small interpolation toolbox used by the Ascent client
// for drag snap-to-cell and snap-back animations.
//
// Pure Go, no syscall/js — exercised by ordinary go test.
package anim

import "math"

// Lerp linearly interpolates between from and to at parameter t in [0, 1].
// Outside that range it extrapolates; callers should clamp t themselves
// when they want clamping behavior.
func Lerp(from, to, t float64) float64 {
	return from + (to-from)*t
}

// EaseOutCubic returns the eased value of t in [0, 1]. Outside the range it
// clamps. Cubic ease-out feels natural for "stone settling into place" — the
// motion is fast at first and decelerates.
func EaseOutCubic(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	u := 1 - t
	return 1 - u*u*u
}

// Progress returns the fraction of an animation that has elapsed at time
// nowMs, given the start time and duration. Clamped to [0, 1]; durations
// of zero or less return 1 immediately so degenerate animations finish.
func Progress(nowMs, startMs, durationMs float64) float64 {
	if durationMs <= 0 {
		return 1
	}
	t := (nowMs - startMs) / durationMs
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

// Animation describes a 2D motion from (FromX, FromY) to (ToX, ToY) in
// generic units. Interpret the units yourself: screen pixels for ghost
// motion, cells for grid-coordinate animations, etc.
type Animation struct {
	FromX, FromY float64
	ToX, ToY     float64
	StartMs      float64
	DurationMs   float64
}

// At returns the interpolated (x, y) position for the animation at time
// nowMs, plus a boolean indicating whether the animation is finished.
//
// Easing is cubic ease-out.
func (a Animation) At(nowMs float64) (x, y float64, done bool) {
	t := Progress(nowMs, a.StartMs, a.DurationMs)
	eased := EaseOutCubic(t)
	x = Lerp(a.FromX, a.ToX, eased)
	y = Lerp(a.FromY, a.ToY, eased)
	done = t >= 1
	return
}

// Distance returns the Euclidean distance between two points.
func Distance(x1, y1, x2, y2 float64) float64 {
	dx, dy := x2-x1, y2-y1
	return math.Sqrt(dx*dx + dy*dy)
}
