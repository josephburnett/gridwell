package rpc

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// flagMessages are the messages whose flags the guard holds.
var flagMessages = []protoreflect.MessageDescriptor{
	(&gridwellv1.Tile{}).ProtoReflect().Descriptor(),
	(&gridwellv1.Grid{}).ProtoReflect().Descriptor(),
	(&gridwellv1.PluginGrid{}).ProtoReflect().Descriptor(),
	(&gridwellv1.InfoResponse{}).ProtoReflect().Descriptor(),
	(&pluginv1.InfoResponse{}).ProtoReflect().Descriptor(),
	(&pluginv1.PluginGrid{}).ProtoReflect().Descriptor(),
	(&pluginv1.ListResponse{}).ProtoReflect().Descriptor(),
	(&pluginv1.Entry{}).ProtoReflect().Descriptor(),
}

// TestFlags fails on a bool or enum field Flags does not class, which is a
// dimension of behaviour the parity table cannot know, and on an entry naming
// no field. The count is the axes row of test/boundary/ratchet.txt.
func TestFlags(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		fields := map[string]protoreflect.FieldDescriptor{}
		for _, m := range flagMessages {
			for i := range m.Fields().Len() {
				f := m.Fields().Get(i)
				fields[string(f.FullName())] = f
			}
		}
		classed := map[string]bool{}
		for _, f := range Flags {
			if classed[f.Field] {
				t.Errorf("%s is classed twice", f.Field)
			}
			classed[f.Field] = true
			if fields[f.Field] == nil {
				t.Errorf("%s names no field of the flag messages", f.Field)
			}
			if f.Why == "" || !f.Axis && f.Carrier != "" {
				t.Errorf("%s: every flag says what it changes, and only an axis has a carrier", f.Field)
			}
		}
		for name, f := range fields {
			if k := f.Kind(); (k == protoreflect.BoolKind || k == protoreflect.EnumKind) && !classed[name] {
				t.Errorf("%s is a flag Flags does not class: give it an axis with the parity dimension that carries it, or class it presentation with what it changes", name)
			}
		}
		t.Logf("axes count: %d", uncoveredAxes(Flags))
	})
}

// uncoveredAxes is the axes row's count: the flags classed axis that no
// parity dimension varies on.
func uncoveredAxes(flags []Flag) int {
	n := 0
	for _, f := range flags {
		if f.Axis && f.Carrier == "" {
			n++
		}
	}
	return n
}
