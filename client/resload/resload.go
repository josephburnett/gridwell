// Package resload owns what an asynchronously loaded, revocable resource is:
// the generation guard that discards a superseded result, the revoke that
// keeps one live resource per entry, and the latch that settles an answer
// which never becomes a picture, so it is reported once rather than once per
// frame. The map, the key, the lock and what a new load does to the resource
// an entry already holds stay with each cache.
package resload

import "sync/atomic"

// Resource is a loaded handle the renderer draws.
type Resource interface {
	Truthy() bool
	Revoke()
}

// Entry is one key's resource. Ident is what Res was loaded for, so a cache
// invalidates by identity with no invalidation signal; Failed latches
// FailIdent as answered without a resource, separate from Ident so an entry
// can hold one identity's resource and another's settled miss at once.
type Entry[I comparable] struct {
	Res       Resource
	Ident     I
	Failed    bool
	FailIdent I

	gen int64
}

// nextGen is process-wide, so a generation is never reused: an entry dropped
// and remade while its load ran cannot be hit by that load's result.
var nextGen atomic.Int64

// Ready says the entry holds a usable resource.
func (e *Entry[I]) Ready() bool { return e.Res != nil && e.Res.Truthy() }

// Begin claims the entry for a new load and returns the generation that
// load's result must carry to install.
func (e *Entry[I]) Begin() int64 {
	e.gen = nextGen.Add(1)
	return e.gen
}

// Adopt points the entry at ident and releases what it holds, for a cache
// whose entry answers for the load in flight rather than the resource it had.
func (e *Entry[I]) Adopt(ident I) {
	e.Release()
	e.Ident, e.Failed = ident, false
}

// Settle latches ident as answered without a resource, leaving a loaded one in
// place: it is still what the entry looks like.
func (e *Entry[I]) Settle(ident I) {
	e.Failed, e.FailIdent = true, ident
}

// Release revokes the entry's resource and forgets it.
func (e *Entry[I]) Release() {
	revoke(e.Res)
	e.Res = nil
}

// Take installs res under ident, revoking what it replaces. A nil e, which is
// a key dropped while the load ran, or a generation a later Begin superseded
// revokes res instead and reports false: one of the two always happens, so no
// loaded resource leaks.
func Take[I comparable](e *Entry[I], gen int64, ident I, res Resource) bool {
	if e == nil || e.gen != gen {
		revoke(res)
		return false
	}
	e.Release()
	e.Res, e.Ident, e.Failed = res, ident, false
	return true
}

// Miss settles ident as answered without a resource. A nil or superseded entry
// records nothing and reports false: the newer load owns the answer.
func Miss[I comparable](e *Entry[I], gen int64, ident I) bool {
	if e == nil || e.gen != gen {
		return false
	}
	e.Settle(ident)
	return true
}

func revoke(res Resource) {
	if res != nil && res.Truthy() {
		res.Revoke()
	}
}
