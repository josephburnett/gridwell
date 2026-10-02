package events

import (
	"github.com/josephburnett/gridwell/client/cadence"
	"github.com/josephburnett/gridwell/client/debounce"
)

// Resyncs holds each source's resync until its health has held for
// cadence.HealthSettleMs. A resync cancels and re-reads every grid the source
// serves, so one per flip turns a flapping source into a read storm; the
// resync reads whatever state the source rests in, so one is enough.
type Resyncs struct {
	schedule debounce.Schedule
	now      debounce.Clock
	resync   func(source string)
	by       map[string]*debounce.Debounce
}

// NewResyncs takes the shim's timer and clock and the resync to run.
func NewResyncs(schedule debounce.Schedule, now debounce.Clock, resync func(source string)) *Resyncs {
	return &Resyncs{schedule: schedule, now: now, resync: resync, by: map[string]*debounce.Debounce{}}
}

// Transition arms source's resync, restarting its wait.
func (r *Resyncs) Transition(source string) {
	d, ok := r.by[source]
	if !ok {
		d = debounce.New(r.schedule, r.now, cadence.HealthSettleMode, func() { r.resync(source) })
		r.by[source] = d
	}
	d.Arm(cadence.HealthSettleMs)
}
