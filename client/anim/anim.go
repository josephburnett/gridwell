// Package anim is the small interpolation toolbox used by the Gridwell client
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

// SplitN apportions totalMs across an arbitrary number of phases by their
// relative distances. Phases with sub-epsilon distance get zero time. If
// every phase has zero distance, the time is divided equally so the
// caller doesn't end up with a transition that completes instantly.
//
// The last phase absorbs floating-point rounding so the returned values
// always sum to totalMs exactly.
func SplitN(distances []float64, totalMs float64) []float64 {
	out := make([]float64, len(distances))
	if len(distances) == 0 {
		return out
	}
	var sum float64
	for _, d := range distances {
		if d > 1e-6 {
			sum += d
		}
	}
	if sum < 1e-6 {
		per := totalMs / float64(len(distances))
		for i := range out {
			out[i] = per
		}
		return out
	}
	var allocated float64
	for i, d := range distances {
		if d > 1e-6 {
			out[i] = totalMs * d / sum
			allocated += out[i]
		}
	}
	out[len(out)-1] += totalMs - allocated
	return out
}

// LerpExp interpolates between from and to in log space at parameter t.
// Used for zoom transitions: linear interpolation feels visually
// non-uniform because perceived "zoom level" is logarithmic in scale.
//
// Both from and to must be strictly positive; otherwise the result is the
// linear interpolation as a fallback.
func LerpExp(from, to, t float64) float64 {
	if from <= 0 || to <= 0 {
		return Lerp(from, to, t)
	}
	return from * math.Pow(to/from, t)
}
