// Command gen writes api/rpc's Go record types and their proto conversions
// FROM the proto descriptor. It is a protoc plugin, run by `buf generate`
// alongside protoc-gen-go, so the one command that regenerates the wire
// regenerates its Go mirror too and `make proto-check` fails when either is
// stale.
//
// The proto is the owner of the record shapes; deriving the Go side from
// it leaves nothing to drift.
//
// What it does not generate: the Go shapes that are deliberately not a
// message mirror — the Event discriminator over the proto's oneof, the
// embedded Framing, and the typed create/set sugar over the unified
// CreateTile/SetTile verbs. Those live in types.go and conv.go and carry
// their own reasons.
package main

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/pluginpb"
)

// sourceFile is the one proto this program mirrors.
const sourceFile = "gridwell/v1/data.proto"

// outFile is where the mirror lands, relative to buf.gen.yaml's out (the
// repo root).
const outFile = "api/rpc/wire_gen.go"

// pbImport is the generated protobuf package the conversions target.
const pbImport = "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

// mirror names one proto message that has a Go twin in package rpc.
//
// This table is the only hand-maintained part of the mirror, and it holds no
// field facts: adding a field to a message is a proto edit and nothing else.
// A message lands here when the Go side wants a plain-struct twin, for value
// semantics: pb messages carry a sync.Mutex in their state and cannot be
// copied, which is why the client's caches hold rpc.Tile and not pb.Tile.
//
// to/from say which conversion directions ship. Both are usually true; a
// direction no binary calls is dead code, which
// scripts/check-deadcode.sh judges in generated code exactly as in
// hand-written code.
type mirror struct {
	msg    string // proto message name
	goName string // Go type name; "" = same as msg
	plural string // name of the slice helpers, e.g. "Tiles" → TilesToProto; "" = none
	to     bool   // emit <goName>ToProto
	from   bool   // emit <goName>FromProto
}

var mirrors = []mirror{
	// The records.
	{msg: "Tile", plural: "Tiles", to: true, from: true},
	{msg: "Grid", to: true, from: true},
	{msg: "MenuEntry", plural: "MenuEntries", to: true, from: true},
	{msg: "PluginInfo", plural: "PluginInfos", to: true, from: true},
	{msg: "ConnectionInfo", plural: "ConnectionInfos", to: true, from: true},
	{msg: "SearchResult", plural: "SearchResults", to: true, from: true},

	// Requests and responses whose Go shape is the message, field for field.
	{msg: "GetGridResponse", to: true, from: true},
	{msg: "CloneTileRequest", to: true, from: true},
	{msg: "PlaceTileRequest", to: true, from: true},
	{msg: "DeleteTileRequest", to: true, from: true},
	{msg: "ShellSessionAliveRequest", to: true},
	{msg: "ShellSessionAliveResponse", from: true},

	// The event payloads. Event itself is hand-written: its proto oneof
	// becomes a discriminator + optional pointers on the Go side.
	{msg: "GridChanged", to: true, from: true},
	{msg: "TileChanged", to: true, from: true},
	{msg: "TileRemoved", to: true, from: true},
	{msg: "EventPluginHealth", goName: "PluginHealth", to: true, from: true},
}

// initialisms are the segments a Go name spells in capitals: url_string →
// URLString, node_ns → NodeNS, plugin_uuid → PluginUUID.
var initialisms = map[string]string{
	"id":   "ID",
	"ids":  "IDs",
	"url":  "URL",
	"ns":   "NS",
	"uuid": "UUID",
	"jpeg": "JPEG",
	"pty":  "PTY",
	"html": "HTML",
	"json": "JSON",
}

// goName turns a proto field name into its Go field name.
func goName(protoName string) string {
	var b strings.Builder
	for _, part := range strings.Split(protoName, "_") {
		if part == "" {
			continue
		}
		if up, ok := initialisms[part]; ok {
			b.WriteString(up)
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

func main() {
	protogen.Options{}.Run(func(gen *protogen.Plugin) error {
		// The proto uses proto3 `optional` elsewhere; declare support so buf
		// does not warn that this plugin cannot handle the file it is given.
		gen.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
		for _, f := range gen.Files {
			if !f.Generate || f.Desc.Path() != sourceFile {
				continue
			}
			return generate(gen, f)
		}
		return nil
	})
}

func generate(gen *protogen.Plugin, f *protogen.File) error {
	byName := map[string]mirror{}
	for _, m := range mirrors {
		if m.goName == "" {
			m.goName = m.msg
		}
		byName[m.msg] = m
	}
	msgs := map[string]*protogen.Message{}
	for _, m := range f.Messages {
		msgs[string(m.Desc.Name())] = m
	}
	for _, m := range mirrors {
		if msgs[m.msg] == nil {
			return fmt.Errorf("%s: mirror names message %q, which %s does not declare", outFile, m.msg, sourceFile)
		}
	}

	g := gen.NewGeneratedFile(outFile, protogen.GoImportPath("github.com/josephburnett/gridwell/api/rpc"))
	g.P("// Code generated by api/rpc/internal/gen from ", sourceFile, ". DO NOT EDIT.")
	g.P("//")
	g.P("// The proto is the one description of these records. Change a field")
	g.P("// there and run `buf generate` (make proto-check does it for you);")
	g.P("// editing this file by hand is undone by the next regeneration.")
	g.P()
	g.P("package rpc")
	g.P()
	g.P("import pb ", `"`+pbImport+`"`)
	g.P()

	// Deterministic output: emit in the table's order, which follows the
	// proto's own grouping.
	for _, m := range mirrors {
		if m.goName == "" {
			m.goName = m.msg
		}
		if err := emitStruct(g, msgs[m.msg], m, byName); err != nil {
			return err
		}
	}
	for _, m := range mirrors {
		if m.goName == "" {
			m.goName = m.msg
		}
		if err := emitConversions(g, msgs[m.msg], m, byName); err != nil {
			return err
		}
	}
	return nil
}

// emitStruct writes the Go struct for one mirrored message, carrying the
// proto's own documentation across.
func emitStruct(g *protogen.GeneratedFile, msg *protogen.Message, m mirror, byName map[string]mirror) error {
	emitComments(g, msg.Comments)
	if msg.Comments.Leading == "" {
		g.P("// ", m.goName, " mirrors ", sourceFile, "'s ", msg.Desc.Name(), ".")
	}
	g.P("type ", m.goName, " struct {")
	for _, field := range msg.Fields {
		emitComments(g, field.Comments)
		typ, err := goType(field, byName)
		if err != nil {
			return err
		}
		g.P(goName(string(field.Desc.Name())), " ", typ,
			" `json:\"", field.Desc.Name(), ",omitempty\"`")
	}
	g.P("}")
	g.P()
	return nil
}

// emitComments writes a proto element's LEADING comment as the Go doc
// comment. Detached blocks are deliberately dropped: they are the proto's
// own bookkeeping about retired field numbers, which the Go mirror has no
// numbers to explain. A comment that documents a FIELD must therefore sit
// directly above it in the proto — nothing else reaches the Go side.
func emitComments(g *protogen.GeneratedFile, c protogen.CommentSet) {
	if c.Leading == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(string(c.Leading), "\n"), "\n") {
		g.P("//", line)
	}
}

// goType is the Go type for one proto field.
func goType(field *protogen.Field, byName map[string]mirror) (string, error) {
	elem, err := goScalarType(field, byName)
	if err != nil {
		return "", err
	}
	if field.Desc.IsList() {
		return "[]" + elem, nil
	}
	return elem, nil
}

func goScalarType(field *protogen.Field, byName map[string]mirror) (string, error) {
	switch field.Desc.Kind() {
	case protoreflect.StringKind:
		return "string", nil
	case protoreflect.BoolKind:
		return "bool", nil
	case protoreflect.Int64Kind:
		return "int64", nil
	case protoreflect.Int32Kind:
		return "int32", nil
	case protoreflect.DoubleKind:
		return "float64", nil
	case protoreflect.FloatKind:
		return "float32", nil
	case protoreflect.BytesKind:
		return "[]byte", nil
	case protoreflect.MessageKind:
		name := string(field.Desc.Message().Name())
		m, ok := byName[name]
		if !ok {
			return "", fmt.Errorf("field %s references message %s, which is not mirrored — add it to the mirrors table",
				field.Desc.FullName(), name)
		}
		return m.goName, nil
	}
	return "", fmt.Errorf("field %s has unsupported kind %s — teach api/rpc/internal/gen about it",
		field.Desc.FullName(), field.Desc.Kind())
}

// emitConversions writes <T>ToProto / <T>FromProto and, when the mirror
// declares a plural, the slice helpers over them. nil converts to nil in
// both directions: callers pass "no tile" straight through without
// allocating an empty one.
func emitConversions(g *protogen.GeneratedFile, msg *protogen.Message, m mirror, byName map[string]mirror) error {
	if m.to {
		g.P("// ", m.goName, "ToProto converts a ", m.goName, " to its wire form.")
		g.P("func ", m.goName, "ToProto(v *", m.goName, ") *pb.", msg.Desc.Name(), " {")
		g.P("if v == nil { return nil }")
		g.P("return &pb.", msg.Desc.Name(), "{")
		for _, field := range msg.Fields {
			expr, err := toProtoExpr(field, byName)
			if err != nil {
				return err
			}
			g.P(field.GoName, ": ", expr, ",")
		}
		g.P("}")
		g.P("}")
		g.P()
		if m.plural != "" {
			g.P("// ", m.plural, "ToProto converts a slice of ", m.goName, " to wire form.")
			g.P("func ", m.plural, "ToProto(vs []", m.goName, ") []*pb.", msg.Desc.Name(), " {")
			g.P("if vs == nil { return nil }")
			g.P("out := make([]*pb.", msg.Desc.Name(), ", len(vs))")
			g.P("for i := range vs { out[i] = ", m.goName, "ToProto(&vs[i]) }")
			g.P("return out")
			g.P("}")
			g.P()
		}
	}
	if m.from {
		g.P("// ", m.goName, "FromProto converts a wire ", msg.Desc.Name(), " back.")
		g.P("func ", m.goName, "FromProto(p *pb.", msg.Desc.Name(), ") *", m.goName, " {")
		g.P("if p == nil { return nil }")
		g.P("out := &", m.goName, "{")
		var deferred []*protogen.Field
		for _, field := range msg.Fields {
			expr, direct, err := fromProtoExpr(field, byName)
			if err != nil {
				return err
			}
			if !direct {
				deferred = append(deferred, field)
				continue
			}
			g.P(goName(string(field.Desc.Name())), ": ", expr, ",")
		}
		g.P("}")
		// Singular message fields are VALUES on the Go side, so a nil on the
		// wire leaves the zero value rather than dereferencing nil.
		for _, field := range deferred {
			sub := byName[string(field.Desc.Message().Name())]
			g.P("if v := ", sub.goName, "FromProto(p.", field.GoName, "); v != nil { out.",
				goName(string(field.Desc.Name())), " = *v }")
		}
		g.P("return out")
		g.P("}")
		g.P()
		if m.plural != "" {
			g.P("// ", m.plural, "FromProto converts a slice of wire ", msg.Desc.Name(), " back.")
			g.P("func ", m.plural, "FromProto(ps []*pb.", msg.Desc.Name(), ") []", m.goName, " {")
			g.P("if ps == nil { return nil }")
			g.P("out := make([]", m.goName, ", len(ps))")
			g.P("for i, p := range ps { if v := ", m.goName, "FromProto(p); v != nil { out[i] = *v } }")
			g.P("return out")
			g.P("}")
			g.P()
		}
	}
	return nil
}

func toProtoExpr(field *protogen.Field, byName map[string]mirror) (string, error) {
	name := goName(string(field.Desc.Name()))
	if field.Desc.Kind() != protoreflect.MessageKind {
		return "v." + name, nil
	}
	sub, ok := byName[string(field.Desc.Message().Name())]
	if !ok {
		return "", fmt.Errorf("field %s references unmirrored message", field.Desc.FullName())
	}
	if field.Desc.IsList() {
		if sub.plural == "" {
			return "", fmt.Errorf("field %s is a repeated %s — give that mirror a plural",
				field.Desc.FullName(), sub.goName)
		}
		return sub.plural + "ToProto(v." + name + ")", nil
	}
	if !sub.to {
		return "", fmt.Errorf("field %s needs %sToProto — set to:true on that mirror",
			field.Desc.FullName(), sub.goName)
	}
	return sub.goName + "ToProto(&v." + name + ")", nil
}

// fromProtoExpr returns the expression and whether it can be written in the
// composite literal (singular message fields cannot: they need a nil check).
func fromProtoExpr(field *protogen.Field, byName map[string]mirror) (expr string, direct bool, err error) {
	if field.Desc.Kind() != protoreflect.MessageKind {
		return "p." + field.GoName, true, nil
	}
	sub, ok := byName[string(field.Desc.Message().Name())]
	if !ok {
		return "", false, fmt.Errorf("field %s references unmirrored message", field.Desc.FullName())
	}
	if field.Desc.IsList() {
		if sub.plural == "" {
			return "", false, fmt.Errorf("field %s is a repeated %s — give that mirror a plural",
				field.Desc.FullName(), sub.goName)
		}
		return sub.plural + "FromProto(p." + field.GoName + ")", true, nil
	}
	if !sub.from {
		return "", false, fmt.Errorf("field %s needs %sFromProto — set from:true on that mirror",
			field.Desc.FullName(), sub.goName)
	}
	return "", false, nil
}
