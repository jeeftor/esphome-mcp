package native

import "testing"

func TestProtoBuilderAndReader(t *testing.T) {
	var b protoBuilder
	b.string(1, "esphome-mcp")
	b.uint32(2, 1)
	b.uint32(3, 14)
	b.fixed32(99, 0xdeadbeef)

	r := newProtoReader(b.buf)
	want := []struct {
		field int
		kind  string
		val   string
	}{
		{1, "string", "esphome-mcp"},
		{2, "varint", "1"},
		{3, "varint", "14"},
		{99, "fixed32", "3735928559"},
	}
	for i, w := range want {
		field, wire, ok := r.next()
		if !ok {
			t.Fatalf("field %d: no more fields", i)
		}
		if field != w.field {
			t.Errorf("field %d: got field %d, want %d", i, field, w.field)
		}
		switch w.kind {
		case "string":
			if wire != wireBytes {
				t.Errorf("field %d: wire %d, want bytes", i, wire)
			}
			s, _ := r.string()
			if s != w.val {
				t.Errorf("field %d: got %q, want %q", i, s, w.val)
			}
		case "varint":
			if wire != wireVarint {
				t.Errorf("field %d: wire %d, want varint", i, wire)
			}
			v, _ := r.varint()
			if v != 1 && v != 14 {
				t.Errorf("field %d: got %d", i, v)
			}
		case "fixed32":
			if wire != wireFixed32 {
				t.Errorf("field %d: wire %d, want fixed32", i, wire)
			}
			v, _ := r.fixed32()
			if v != 0xdeadbeef {
				t.Errorf("field %d: got %#x, want %#x", i, v, 0xdeadbeef)
			}
		}
	}
}

func TestDecodeEntityCommon(t *testing.T) {
	var b protoBuilder
	b.fixed32(1, 42)
	b.string(2, "temp_sensor")
	b.string(3, "Temperature")
	b.string(4, "unique-123")
	// extra unknown field that must be skipped
	b.tag(9, wireVarint)
	b.varint(7)

	key, objectID, name, uniqueID := decodeEntityCommon(b.buf)
	if key != 42 || objectID != "temp_sensor" || name != "Temperature" || uniqueID != "unique-123" {
		t.Errorf("decodeEntityCommon = %d %q %q %q", key, objectID, name, uniqueID)
	}
}

func TestDecodeStateFloatAndBool(t *testing.T) {
	var fb protoBuilder
	fb.fixed32(1, 7)
	fb.tag(2, wireFixed32)
	fb.buf = append(fb.buf, 0, 0, 0x80, 0x3f) // 1.0 as little-endian float32
	k, s, missing := decodeStateFloat(fb.buf)
	if k != 7 || s != 1.0 || missing {
		t.Errorf("decodeStateFloat = %d %v %v", k, s, missing)
	}

	var bb protoBuilder
	bb.fixed32(1, 9)
	bb.tag(2, wireVarint)
	bb.varint(1)
	k2, st := decodeStateBool(bb.buf)
	if k2 != 9 || !st {
		t.Errorf("decodeStateBool = %d %v", k2, st)
	}
}
