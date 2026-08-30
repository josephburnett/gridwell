package rpc

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// The wire is frozen and these tests are the pin. The Go record types
// (rpc.Tile, rpc.Grid) travel to the client as protojson over the generated
// pb messages, through connect.WithProtoJSON in NewDefaultClient, so the
// JSON names on the wire are the proto field names, not the json tags on the
// Go structs. Two properties are locked here:
//
//   - An exhaustive round trip. The fixture is built by reflection over the
//     Go struct, so it fills every field automatically and a converter that
//     drops a newly added field cannot round-trip green.
//   - The JSON shape is golden. api/rpc/testdata/*.json records the exact
//     field names and values a fully-populated record marshals to; the client
//     and the e2e specs read those names, so a rename is a wire break and
//     must fail here.

// fill populates every exported field of the struct pointed at by v with a
// distinct non-zero value, so a converter that drops or swaps a field cannot
// round-trip clean.
//
// The value is derived from the field name, never from its position. That is
// what makes the golden a wire pin rather than a struct-layout pin:
// reordering the Go fields leaves the golden byte-identical, while renaming
// or dropping one changes it.
func fill(t *testing.T, v reflect.Value) {
	t.Helper()
	rt := v.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		n := int64(crc32.ChecksumIEEE([]byte(f.Name))%1000) + 1
		fv := v.Field(i)
		switch f.Type.Kind() {
		case reflect.String:
			fv.SetString(fmt.Sprintf("%s-%d", f.Name, n))
		case reflect.Int64, reflect.Int32, reflect.Int:
			fv.SetInt(n)
		case reflect.Float64:
			fv.SetFloat(float64(n) + 0.5)
		case reflect.Bool:
			fv.SetBool(true)
		case reflect.Slice:
			if f.Type.Elem().Kind() != reflect.Struct {
				t.Fatalf("fill: %s.%s is a slice of %s — extend fill", rt.Name(), f.Name, f.Type.Elem().Kind())
			}
			elem := reflect.New(f.Type.Elem()).Elem()
			fill(t, elem)
			fv.Set(reflect.Append(fv, elem))
		case reflect.Struct:
			fill(t, fv)
		default:
			t.Fatalf("fill: %s.%s has unhandled kind %s — extend fill", rt.Name(), f.Name, f.Type.Kind())
		}
	}
}

// exhaustiveTile returns a Tile with every field set to a distinct value.
func exhaustiveTile(t *testing.T) *Tile {
	t.Helper()
	var out Tile
	fill(t, reflect.ValueOf(&out).Elem())
	return &out
}

// exhaustiveGrid returns a Grid with every field set to a distinct value.
func exhaustiveGrid(t *testing.T) *Grid {
	t.Helper()
	var out Grid
	fill(t, reflect.ValueOf(&out).Elem())
	return &out
}

// TestTileWireRoundTrip: rpc.Tile → pb → the Connect JSON codec → pb →
// rpc.Tile is the identity for a tile with every field set.
func TestTileWireRoundTrip(t *testing.T) {
	in := exhaustiveTile(t)
	if got := TileFromProto(throughJSON(t, TileToProto(in))); !reflect.DeepEqual(in, got) {
		t.Errorf("tile wire round-trip diverged:\n in = %+v\nout = %+v", in, got)
	}
}

// TestGridWireRoundTrip: the same for a grid (menu entries included).
func TestGridWireRoundTrip(t *testing.T) {
	in := exhaustiveGrid(t)
	if got := GridFromProto(throughJSON(t, GridToProto(in))); !reflect.DeepEqual(in, got) {
		t.Errorf("grid wire round-trip diverged:\n in = %+v\nout = %+v", in, got)
	}
}

// throughJSON marshals m the way the Connect JSON codec does and reads it
// back — the actual encoding between the server and the wasm client.
func throughJSON[M proto.Message](t *testing.T, m M) M {
	t.Helper()
	b, err := protojson.Marshal(m)
	if err != nil {
		t.Fatalf("protojson marshal: %v", err)
	}
	out := m.ProtoReflect().New().Interface().(M)
	if err := protojson.Unmarshal(b, out); err != nil {
		t.Fatalf("protojson unmarshal: %v", err)
	}
	return out
}

// TestTileJSONGolden and TestGridJSONGolden pin the JSON field names and
// value encodings of a fully-populated record. Update the golden only with a
// deliberate wire change, never for a Go-side refactor.
func TestTileJSONGolden(t *testing.T) {
	checkGolden(t, "tile.json", TileToProto(exhaustiveTile(t)))
}

func TestGridJSONGolden(t *testing.T) {
	checkGolden(t, "grid.json", GridToProto(exhaustiveGrid(t)))
}

// checkGolden compares m's Connect-JSON encoding with testdata/<name>.
// protojson deliberately randomizes its whitespace, so the bytes are
// re-normalized through encoding/json, which sorts map keys, before the
// comparison. What is pinned is the names and values, not the spacing.
func checkGolden(t *testing.T, name string, m proto.Message) {
	t.Helper()
	raw, err := protojson.Marshal(m)
	if err != nil {
		t.Fatalf("protojson marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("re-read protojson: %v", err)
	}
	got, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if os.Getenv("GRIDWELL_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with GRIDWELL_UPDATE_GOLDEN=1)", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("wire JSON changed — the client and the e2e specs read these names.\n got:\n%s\nwant:\n%s", got, want)
	}
}
