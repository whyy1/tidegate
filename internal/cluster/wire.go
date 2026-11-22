package cluster

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

// Wire types as tag low bits. A decoder key is fieldNumber<<3 | wireType,
// which is exactly the encoded tag value.
const (
	wVarint  = uint64(protowire.VarintType)
	wFixed64 = uint64(protowire.Fixed64Type)
	wBytes   = uint64(protowire.BytesType)
)

// message is implemented by every protocol message. size must return the
// exact length appendTo produces. unmarshal merges b into the receiver with
// protobuf semantics: scalars overwrite, repeated fields append.
type message interface {
	size() int
	appendTo(b []byte) []byte
	unmarshal(b []byte) error
}

func marshal(m message) ([]byte, error) {
	n := m.size()
	b := m.appendTo(make([]byte, 0, n))
	if len(b) != n {
		return nil, fmt.Errorf("cluster: %T encoded %d bytes, expected %d", m, len(b), n)
	}
	return b, nil
}

// validUTF8 replaces invalid UTF-8 so peers using generated proto3 code,
// which reject invalid strings, can decode every message.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

func sizeString(num protowire.Number, s string) int {
	if s == "" {
		return 0
	}
	return protowire.SizeTag(num) + protowire.SizeBytes(len(validUTF8(s)))
}

func appendString(b []byte, num protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, validUTF8(s))
}

func sizeBytes(num protowire.Number, v []byte) int {
	if len(v) == 0 {
		return 0
	}
	return protowire.SizeTag(num) + protowire.SizeBytes(len(v))
}

func appendBytes(b []byte, num protowire.Number, v []byte) []byte {
	if len(v) == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func sizeUint(num protowire.Number, v uint64) int {
	if v == 0 {
		return 0
	}
	return protowire.SizeTag(num) + protowire.SizeVarint(v)
}

func appendUint(b []byte, num protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

// int64 and enum fields are plain varints of the two's complement value;
// int32 values are sign extended to 64 bits first, as protobuf requires.
func sizeInt64(num protowire.Number, v int64) int { return sizeUint(num, uint64(v)) }

func appendInt64(b []byte, num protowire.Number, v int64) []byte {
	return appendUint(b, num, uint64(v))
}

func sizeInt32(num protowire.Number, v int32) int { return sizeUint(num, uint64(int64(v))) }

func appendInt32(b []byte, num protowire.Number, v int32) []byte {
	return appendUint(b, num, uint64(int64(v)))
}

func sizeDouble(num protowire.Number, v float64) int {
	if math.Float64bits(v) == 0 {
		return 0
	}
	return protowire.SizeTag(num) + protowire.SizeFixed64()
}

func appendDouble(b []byte, num protowire.Number, v float64) []byte {
	if math.Float64bits(v) == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, math.Float64bits(v))
}

// Embedded message fields are always written, even when empty, so a oneof
// member with no fields set is still present on the wire.
func sizeMessage(num protowire.Number, m message) int {
	return protowire.SizeTag(num) + protowire.SizeBytes(m.size())
}

func appendMessage(b []byte, num protowire.Number, m message) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	b = protowire.AppendVarint(b, uint64(m.size()))
	return m.appendTo(b)
}

func unixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromUnixNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// decoder walks the fields of one message.
type decoder struct {
	b   []byte
	err error
	key uint64
	num protowire.Number
	typ protowire.Type
}

func (d *decoder) fail(code int) {
	if d.err == nil {
		d.err = fmt.Errorf("cluster: malformed message: %w", protowire.ParseError(code))
	}
	d.b = nil
}

// next reads the next tag into d.key. It returns false at the end of the
// input or after an error.
func (d *decoder) next() bool {
	if d.err != nil || len(d.b) == 0 {
		return false
	}
	num, typ, n := protowire.ConsumeTag(d.b)
	if n < 0 {
		d.fail(n)
		return false
	}
	d.b = d.b[n:]
	d.num, d.typ = num, typ
	d.key = uint64(num)<<3 | uint64(typ)
	return true
}

// skip discards the value of an unknown field, or of a known field sent with
// an unexpected wire type, which protobuf also treats as unknown.
func (d *decoder) skip() {
	n := protowire.ConsumeFieldValue(d.num, d.typ, d.b)
	if n < 0 {
		d.fail(n)
		return
	}
	d.b = d.b[n:]
}

// bytes returns a length-delimited value. The result aliases the input.
func (d *decoder) bytes() []byte {
	v, n := protowire.ConsumeBytes(d.b)
	if n < 0 {
		d.fail(n)
		return nil
	}
	d.b = d.b[n:]
	if len(v) == 0 {
		return nil
	}
	return v
}

func (d *decoder) string() string { return string(d.bytes()) }

func (d *decoder) varint() uint64 {
	v, n := protowire.ConsumeVarint(d.b)
	if n < 0 {
		d.fail(n)
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *decoder) double() float64 {
	v, n := protowire.ConsumeFixed64(d.b)
	if n < 0 {
		d.fail(n)
		return 0
	}
	d.b = d.b[n:]
	return math.Float64frombits(v)
}

// message decodes an embedded message into m, merging with its contents.
func (d *decoder) message(m message) {
	v := d.bytes()
	if d.err != nil {
		return
	}
	if err := m.unmarshal(v); err != nil {
		d.err = err
		d.b = nil
	}
}
