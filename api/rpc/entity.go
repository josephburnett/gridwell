package rpc

import (
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// EntityOf is a request's primary entity, the kv["id"] both halves of an rpc
// span record: its first string field named *_id that is set, in declaration
// order. Every request spells its tile or grid that way, so a verb the service
// gains is covered with no table to grow.
func EntityOf(msg any) string {
	m, ok := msg.(proto.Message)
	if !ok {
		return ""
	}
	r := m.ProtoReflect()
	fields := r.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.StringKind || fd.IsList() || !strings.HasSuffix(string(fd.Name()), "_id") {
			continue
		}
		if v := r.Get(fd).String(); v != "" {
			return v
		}
	}
	return ""
}
