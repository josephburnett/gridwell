package pluginhost

import (
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// absentVerbs is the optional verbs the running plugin process answered
// Unimplemented, each with that answer, so a verb it lacks costs one round
// trip per process rather than one per tile. Leaving a verb out is legal and
// undeclared (docs/plugin-authoring.md), and a process cannot gain a method,
// so the memory lives exactly as long as one: forget runs on every supervisor
// transition.
type absentVerbs struct {
	mu     sync.Mutex
	epoch  uint64
	absent map[string]error
}

func (v *absentVerbs) forget() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.epoch++
	v.absent = nil
}

// ask answers the remembered refusal, reason included, or runs call and
// remembers an Unimplemented it answers, unless the process changed while it
// ran.
func ask[T any](v *absentVerbs, verb string, call func() (T, error)) (T, error) {
	v.mu.Lock()
	if err, ok := v.absent[verb]; ok {
		v.mu.Unlock()
		var zero T
		return zero, err
	}
	epoch := v.epoch
	v.mu.Unlock()
	out, err := call()
	if status.Code(err) == codes.Unimplemented {
		v.mu.Lock()
		if v.epoch == epoch {
			if v.absent == nil {
				v.absent = map[string]error{}
			}
			v.absent[verb] = err
		}
		v.mu.Unlock()
	}
	return out, err
}
