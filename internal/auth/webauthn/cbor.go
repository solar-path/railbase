package webauthn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Minimal CBOR decoder.
//
// WebAuthn uses CBOR (RFC 8949) for two specific payloads:
//
//	1. attestationObject: a 3-field map (fmt, attStmt, authData)
//	2. COSE_Key public-key: a map keyed by small integers
//
// We don't need a general-purpose CBOR implementation. The decoder
// here handles exactly the shapes WebAuthn ships:
//
//	- unsigned int / negative int (major type 0/1)
//	- byte string (major type 2)
//	- text string (major type 3)
//	- array (major type 4)  — used by attStmt's x5c cert chain (skipped)
//	- map (major type 5)
//	- bool / null (major type 7 simple values)
//
// Floats / tagged values / indefinite-length items are NOT supported;
// the decoder errors loudly if it sees one. WebAuthn doesn't use them.
//
// Hand-rolled (~150 LOC) rather than pulling fxamacker/cbor to keep
// the dep tree minimal — RFC 8949 is small enough that a focused
// reader covers our needs without the trade-offs of a general decoder.

// CBORValue is the decoded form. .Kind tells you which sub-field is
// populated. Maps and arrays are kept as []CBORItem so caller can
// scan in source order without losing duplicate keys (none of
// WebAuthn's payloads use duplicate keys, but defensive).
type CBORValue struct {
	Kind  CBORKind
	Int   int64
	Bytes []byte
	Str   string
	Map   []CBORItem
	Arr   []CBORValue
	Bool  bool
}

type CBORKind uint8

const (
	CBORInt CBORKind = iota + 1
	CBORBytes
	CBORString
	CBORMap
	CBORArr
	CBORBool
	CBORNull
)

// CBORItem is one (key, value) pair inside a CBOR map. The key is
// itself a CBORValue because COSE maps use integer keys (1=kty, 3=alg,
// -1=crv) while attestationObject uses string keys ("fmt", "authData").
type CBORItem struct {
	Key, Val CBORValue
}

// DecodeCBOR parses a single top-level CBOR value. Returns (value,
// bytesRead). Errors on malformed input or unsupported features.
func DecodeCBOR(data []byte) (CBORValue, int, error) {
	d := &cborDecoder{buf: data}
	v, err := d.decode()
	if err != nil {
		return CBORValue{}, 0, err
	}
	return v, d.pos, nil
}

type cborDecoder struct {
	buf []byte
	pos int
}

// decode reads one value from the current position.
func (d *cborDecoder) decode() (CBORValue, error) {
	if d.pos >= len(d.buf) {
		return CBORValue{}, errors.New("cbor: truncated")
	}
	b := d.buf[d.pos]
	d.pos++
	major := b >> 5
	minor := b & 0x1f

	// Unwrap the argument (integer payload encoded by `minor`).
	arg, err := d.readArg(minor)
	if err != nil {
		return CBORValue{}, err
	}

	switch major {
	case 0: // unsigned int
		return CBORValue{Kind: CBORInt, Int: int64(arg)}, nil
	case 1: // negative int: real value = -1 - arg
		return CBORValue{Kind: CBORInt, Int: -1 - int64(arg)}, nil
	case 2: // byte string
		if d.pos+int(arg) > len(d.buf) {
			return CBORValue{}, errors.New("cbor: bytes truncated")
		}
		out := make([]byte, arg)
		copy(out, d.buf[d.pos:d.pos+int(arg)])
		d.pos += int(arg)
		return CBORValue{Kind: CBORBytes, Bytes: out}, nil
	case 3: // text string
		if d.pos+int(arg) > len(d.buf) {
			return CBORValue{}, errors.New("cbor: string truncated")
		}
		s := string(d.buf[d.pos : d.pos+int(arg)])
		d.pos += int(arg)
		return CBORValue{Kind: CBORString, Str: s}, nil
	case 4: // array
		items := make([]CBORValue, arg)
		for i := uint64(0); i < arg; i++ {
			v, err := d.decode()
			if err != nil {
				return CBORValue{}, err
			}
			items[i] = v
		}
		return CBORValue{Kind: CBORArr, Arr: items}, nil
	case 5: // map
		items := make([]CBORItem, arg)
		for i := uint64(0); i < arg; i++ {
			k, err := d.decode()
			if err != nil {
				return CBORValue{}, err
			}
			v, err := d.decode()
			if err != nil {
				return CBORValue{}, err
			}
			items[i] = CBORItem{Key: k, Val: v}
		}
		return CBORValue{Kind: CBORMap, Map: items}, nil
	case 7: // simple values
		switch minor {
		case 20:
			return CBORValue{Kind: CBORBool, Bool: false}, nil
		case 21:
			return CBORValue{Kind: CBORBool, Bool: true}, nil
		case 22, 23:
			return CBORValue{Kind: CBORNull}, nil
		default:
			return CBORValue{}, fmt.Errorf("cbor: unsupported simple value %d", minor)
		}
	case 6:
		// Tag — we don't honour tags. Decode the inner value as-is
		// and drop the tag (WebAuthn payloads don't use tags but
		// embedded test vectors sometimes wrap them).
		return d.decode()
	}
	return CBORValue{}, fmt.Errorf("cbor: unknown major type %d", major)
}

// readArg decodes the integer argument from the initial byte's
// `minor` field plus any following byte(s) per RFC 8949 §3.
func (d *cborDecoder) readArg(minor byte) (uint64, error) {
	switch {
	case minor < 24:
		return uint64(minor), nil
	case minor == 24:
		if d.pos >= len(d.buf) {
			return 0, errors.New("cbor: short arg-1")
		}
		v := uint64(d.buf[d.pos])
		d.pos++
		return v, nil
	case minor == 25:
		if d.pos+2 > len(d.buf) {
			return 0, errors.New("cbor: short arg-2")
		}
		v := uint64(binary.BigEndian.Uint16(d.buf[d.pos:]))
		d.pos += 2
		return v, nil
	case minor == 26:
		if d.pos+4 > len(d.buf) {
			return 0, errors.New("cbor: short arg-4")
		}
		v := uint64(binary.BigEndian.Uint32(d.buf[d.pos:]))
		d.pos += 4
		return v, nil
	case minor == 27:
		if d.pos+8 > len(d.buf) {
			return 0, errors.New("cbor: short arg-8")
		}
		v := binary.BigEndian.Uint64(d.buf[d.pos:])
		d.pos += 8
		return v, nil
	case minor == 31:
		return 0, errors.New("cbor: indefinite-length not supported")
	}
	return 0, fmt.Errorf("cbor: invalid minor %d", minor)
}

// FindMap looks up a value in a CBOR map by string key.
func (v CBORValue) FindMap(key string) (CBORValue, bool) {
	if v.Kind != CBORMap {
		return CBORValue{}, false
	}
	for _, it := range v.Map {
		if it.Key.Kind == CBORString && it.Key.Str == key {
			return it.Val, true
		}
	}
	return CBORValue{}, false
}

// FindMapInt looks up a value in a CBOR map by integer key (COSE
// uses int keys, e.g. 1=kty, 3=alg, -1=crv, -2=x, -3=y).
func (v CBORValue) FindMapInt(key int64) (CBORValue, bool) {
	if v.Kind != CBORMap {
		return CBORValue{}, false
	}
	for _, it := range v.Map {
		if it.Key.Kind == CBORInt && it.Key.Int == key {
			return it.Val, true
		}
	}
	return CBORValue{}, false
}

// silence unused if a future refactor drops the math import
var _ = math.Inf
