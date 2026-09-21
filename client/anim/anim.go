// Package anim holds the client's drag and transition interpolation. It is
// pure Go with no syscall/js, so go test runs it.
package anim

import "math"

// Lerp interpolates between from and to at t, extrapolating outside [0, 1].
func Lerp(from, to, t float64) float64 {
	return from + (to-from)*t
}

// EaseOutCubic returns the cubic ease-out of t, clamped to [0, 1].
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

// Progress returns the fraction of an animation elapsed at nowMs, clamped to
// [0, 1]. A duration of zero or less returns 1 so a degenerate animation
// finishes.
func Progress(nowMs, startMs, durationMs float64) float64 {
	if durationMs <= 0 {
		return 1
	}
	return min(max((nowMs-startMs)/durationMs, 0), 1)
}

// Animation is a 2D motion in whatever units the caller chooses.
type Animation struct {
	FromX, FromY float64
	ToX, ToY     float64
	StartMs      float64
	DurationMs   float64
}

// At returns the eased position at nowMs and whether the animation finished.
func (a Animation) At(nowMs float64) (x, y float64, done bool) {
	t := Progress(nowMs, a.StartMs, a.DurationMs)
	eased := EaseOutCubic(t)
	x = Lerp(a.FromX, a.ToX, eased)
	y = Lerp(a.FromY, a.ToY, eased)
	done = t >= 1
	return
}

// SplitN apportions totalMs across phases by relative distance. A phase under
// the epsilon gets zero time; if every phase is, the time divides equally so
// the transition does not complete instantly. The last phase with distance
// absorbs rounding: a negative duration on a zero-distance phase breaks the
// transition stepper.
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
	last := 0
	for i, d := range distances {
		if d > 1e-6 {
			out[i] = totalMs * d / sum
			allocated += out[i]
			last = i
		}
	}
	out[last] += totalMs - allocated
	return out
}

// LerpExp interpolates in log space, because perceived zoom is logarithmic in
// scale. A non-positive end falls back to Lerp.
func LerpExp(from, to, t float64) float64 {
	if from <= 0 || to <= 0 {
		return Lerp(from, to, t)
	}
	return from * math.Pow(to/from, t)
}

// FadeAlpha is the opacity of the ascent trace at nowMs: 1 at startMs, 0 at
// startMs+durationMs.
func FadeAlpha(nowMs, startMs, durationMs float64) float64 {
	r := 1 - Progress(nowMs, startMs, durationMs)
	return r * r
}
