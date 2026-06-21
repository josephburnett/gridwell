# Gridwell

Gridwell is a single-tenant personal operating environment. Tiles live
on a 2D grid; drop one at a coordinate and it stays there. A tile's
preview is what you see when you descend into it; ascend after editing
and the preview shows what you were just looking at.

## The guiding rule: things stay as you left them

**This is the deciding factor.** When a technical decision is unclear,
the option that preserves this principle wins — over performance, over
elegance, over implementation convenience. If a design lets something
change that the user didn't change, the design is wrong.

Gridwell is a physical space. You rearrange it constantly — it is
write-heavy and mutates freely (drop a tile, pan, capture a page,
type) — but **nothing changes except by your explicit action.** Step
out of a room and look back (ascent): you see it exactly as you left
it. Step back in (descent): it is exactly the same. The round trip is
idempotent, and that holds for *everything* — content, view framing,
and layout alike. Reading never mutates.

Four faces of one rule:

1. **Placement is persistent.** A tile at (x, y, w, h) stays until
   something explicitly moves it. No auto-relayout, no resort.
2. **Identity is persistent and stable.** A tile's row id is a
   permanent handle: editing a tile never moves it (mutation is
   in-place; the id never changes), so a reference always returns
   *that* tile. Copies are made only by the explicit **clone** gesture
3. **Preview = descent target = ascent return.** A well's stored
   framing *is* its preview; descending restores it; ascending writes
   it back. One value, read the same way every time.
4. **Mutation is local and reflected.** Every change goes through the
   store, which fans an event to every open view.

## Making changes

No part of Gridwell may be constructed without tests. If you find a bug,
you must answer the question "why was this not caught by a test?". If
you fix a bug, you must write a test that reproduces it. Even if it means
refactoring to make the code testable. There are exceptions to this
rule, small and trivial layers of glue code. But these must be minimized.
I would prefer to slow down and fix test gaps over getting feature
finished.

The codebase must remain DRY. A little extra time to look around and
see if this is a pattern elsewhere will help keep entropy at bay. I
would rather amortize this cost across each commit that have to keep
coming back and asking for the code to be cleaned up. Because I don't
want to have to read the code. I want the code to be written cleanly,
cleary, and accurately. I was it to be simple (not complected) and
to accomplish this is must be DRY.

When you need to make judgement calls about what the right behavior
should be, the primary rule of Gridwell decides. As I move around the
space, nothing should change that I did not change. Previews should
show was I was looking at when inside a tile. Wells should show what
I was looking at when I was inside. Moving between panes should not
cause their contents to change. There are exceptions to this, such as
the menu which actually appears only in the pane currently in focus.
This is by design to show focus. And processes and files outside
Gridwell may come and go outside our control. But things like their
placement, may remain stable as long as they are present.
