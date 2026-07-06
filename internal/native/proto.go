package native

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Minimal protobuf wire-format helpers for the subset of ESPHome native API
// messages we care about. We hand-roll encoding/decoding instead of pulling in
// a full protobuf toolchain to keep the dependency surface tiny.

const (
	wireVarint = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// protoBuilder accumulates protobuf-encoded fields.
type protoBuilder struct{ buf []byte }

func (b *protoBuilder) tag(field, wire int) {
	b.varint(uint64(field<<3 | wire))
}

func (b *protoBuilder) varint(v uint64) {
	for v >= 0x80 {
		b.buf = append(b.buf, byte(v)|0x80)
		v >>= 7
	}
	b.buf = append(b.buf, byte(v))
}

func (b *protoBuilder) string(field int, s string) {
	if s == "" {
		return
	}
	b.tag(field, wireBytes)
	b.varint(uint64(len(s)))
	b.buf = append(b.buf, s...)
}

func (b *protoBuilder) uint32(field int, v uint32) {
	b.tag(field, wireVarint)
	b.varint(uint64(v))
}

func (b *protoBuilder) fixed32(field int, v uint32) {
	b.tag(field, wireFixed32)
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	b.buf = append(b.buf, tmp[:]...)
}

// protoReader iterates over protobuf fields in a message.
type protoReader struct{ buf []byte }

func newProtoReader(buf []byte) *protoReader { return &protoReader{buf: buf} }

// next returns the next (field, wireType) pair, or ok=false at end of message.
func (r *protoReader) next() (field, wire int, ok bool) {
	if len(r.buf) == 0 {
		return 0, 0, false
	}
	v, n := readVarint(r.buf)
	if n <= 0 {
		return 0, 0, false
	}
	r.buf = r.buf[n:]
	return int(v >> 3), int(v & 7), true
}

func (r *protoReader) varint() (uint64, bool) {
	v, n := readVarint(r.buf)
	if n <= 0 {
		return 0, false
	}
	r.buf = r.buf[n:]
	return v, true
}

func (r *protoReader) fixed32() (uint32, bool) {
	if len(r.buf) < 4 {
		return 0, false
	}
	v := binary.LittleEndian.Uint32(r.buf)
	r.buf = r.buf[4:]
	return v, true
}

func (r *protoReader) fixed64() (uint64, bool) {
	if len(r.buf) < 8 {
		return 0, false
	}
	v := binary.LittleEndian.Uint64(r.buf)
	r.buf = r.buf[8:]
	return v, true
}

func (r *protoReader) bytes() ([]byte, bool) {
	v, n := readVarint(r.buf)
	if n <= 0 {
		return nil, false
	}
	r.buf = r.buf[n:]
	if uint64(len(r.buf)) < v {
		return nil, false
	}
	out := r.buf[:v]
	r.buf = r.buf[v:]
	return out, true
}

func (r *protoReader) string() (string, bool) {
	b, ok := r.bytes()
	if !ok {
		return "", false
	}
	return string(b), true
}

// skip advances past the current field's value.
func (r *protoReader) skip(wire int) {
	switch wire {
	case wireVarint:
		_, _ = r.varint()
	case wireFixed32:
		_, _ = r.fixed32()
	case wireFixed64:
		_, _ = r.fixed64()
	case wireBytes:
		_, _ = r.bytes()
	}
}

func readVarint(buf []byte) (uint64, int) {
	var v uint64
	var shift uint
	for i, b := range buf {
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, i + 1
		}
		shift += 7
		if shift > 63 {
			return 0, 0
		}
	}
	return 0, 0
}

// float32bits converts a fixed32 to a float32.
func float32bits(v uint32) float32 { return math.Float32frombits(v) }

// decodeEntityCommon extracts the shared key/object_id/name/unique_id fields
// present on every ListEntities*Response message.
func decodeEntityCommon(buf []byte) (key uint32, objectID, name, uniqueID string) {
	r := newProtoReader(buf)
	for {
		field, wire, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1: // key (fixed32)
			if v, ok := r.fixed32(); ok {
				key = v
			}
		case 2: // object_id (string)
			if s, ok := r.string(); ok {
				objectID = s
			}
		case 3: // name (string)
			if s, ok := r.string(); ok {
				name = s
			}
		case 4: // unique_id (string)
			if s, ok := r.string(); ok {
				uniqueID = s
			}
		default:
			r.skip(wire)
		}
	}
	return key, objectID, name, uniqueID
}

// decodeStateString extracts key (field 1, fixed32) and a string state
// (field 2). missing_state (field 3, bool) is reported when present.
func decodeStateString(buf []byte) (key uint32, state string, missing bool) {
	r := newProtoReader(buf)
	for {
		field, wire, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			if v, ok := r.fixed32(); ok {
				key = v
			}
		case 2:
			if s, ok := r.string(); ok {
				state = s
			}
		case 3:
			if v, ok := r.varint(); ok {
				missing = v != 0
			}
		default:
			r.skip(wire)
		}
	}
	return key, state, missing
}

// decodeStateBool extracts key (field 1, fixed32) and a bool state (field 2).
func decodeStateBool(buf []byte) (key uint32, state bool) {
	r := newProtoReader(buf)
	for {
		field, wire, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			if v, ok := r.fixed32(); ok {
				key = v
			}
		case 2:
			if v, ok := r.varint(); ok {
				state = v != 0
			}
		default:
			r.skip(wire)
		}
	}
	return key, state
}

// decodeStateFloat extracts key (field 1, fixed32) and a float state (field 2).
func decodeStateFloat(buf []byte) (key uint32, state float32, missing bool) {
	r := newProtoReader(buf)
	for {
		field, wire, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			if v, ok := r.fixed32(); ok {
				key = v
			}
		case 2:
			if v, ok := r.fixed32(); ok {
				state = float32bits(v)
			}
		case 3:
			if v, ok := r.varint(); ok {
				missing = v != 0
			}
		default:
			r.skip(wire)
		}
	}
	return key, state, missing
}

// decodeStateEnum extracts key (field 1, fixed32) and an enum/int state (field 2).
func decodeStateEnum(buf []byte) (key uint32, state int32) {
	r := newProtoReader(buf)
	for {
		field, wire, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			if v, ok := r.fixed32(); ok {
				key = v
			}
		case 2:
			if v, ok := r.varint(); ok {
				state = int32(v)
			}
		default:
			r.skip(wire)
		}
	}
	return key, state
}

func formatFloat(f float32) string {
	return fmt.Sprintf("%g", f)
}
