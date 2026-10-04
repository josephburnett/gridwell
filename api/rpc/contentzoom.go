package rpc

import (
	"fmt"
	"math"
)

// The declared range of a tile's content zoom; see NewContentZoom.
const (
	ContentZoomMin = 0.5
	ContentZoomMax = 3.0
)

// ContentZoom is a content scale a writer may store. The column's 0 is a row
// never zoomed, which no write produces: a reset writes 1.
type ContentZoom struct{ v float64 }

// NewContentZoom checks a wire value: finite and within the declared range.
func NewContentZoom(z float64) (ContentZoom, error) {
	if math.IsNaN(z) || z < ContentZoomMin || z > ContentZoomMax {
		return ContentZoom{}, fmt.Errorf("content_zoom must be a number from %g to %g, not %g",
			ContentZoomMin, ContentZoomMax, z)
	}
	return ContentZoom{z}, nil
}

// Float is the value to store.
func (z ContentZoom) Float() float64 { return z.v }
