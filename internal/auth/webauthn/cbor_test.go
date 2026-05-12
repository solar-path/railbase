package webauthn

import (
	"bytes"
	"testing"
)

func TestCBORDecodeBasicTypes(t *testing.T) {
	// Hand-built CBOR vectors (RFC 8949 §A "Examples"):
	cases := []struct {
		name     string
		bytes    []byte
		wantKind CBORKind
		check    func(*testing.T, CBORValue)
	}{
		{
			name:     "uint 0",
			bytes:    []byte{0x00},
			wantKind: CBORInt,
			check:    func(t *testing.T, v CBORValue) { mustEq(t, v.Int, int64(0)) },
		},
		{
			name:     "uint 24",
			bytes:    []byte{0x18, 0x18},
			wantKind: CBORInt,
			check:    func(t *testing.T, v CBORValue) { mustEq(t, v.Int, int64(24)) },
		},
		{
			name:     "neg -1",
			bytes:    []byte{0x20},
			wantKind: CBORInt,
			check:    func(t *testing.T, v CBORValue) { mustEq(t, v.Int, int64(-1)) },
		},
		{
			name:     "neg -7 (COSE ES256 alg id)",
			bytes:    []byte{0x26},
			wantKind: CBORInt,
			check:    func(t *testing.T, v CBORValue) { mustEq(t, v.Int, int64(-7)) },
		},
		{
			name:     "bytes 'hi'",
			bytes:    []byte{0x42, 'h', 'i'},
			wantKind: CBORBytes,
			check: func(t *testing.T, v CBORValue) {
				if !bytes.Equal(v.Bytes, []byte("hi")) {
					t.Errorf("got %q, want hi", v.Bytes)
				}
			},
		},
		{
			name:     "text 'fmt'",
			bytes:    []byte{0x63, 'f', 'm', 't'},
			wantKind: CBORString,
			check:    func(t *testing.T, v CBORValue) { mustEq(t, v.Str, "fmt") },
		},
		{
			name:  "map {fmt: 'none'}",
			bytes: []byte{0xa1, 0x63, 'f', 'm', 't', 0x64, 'n', 'o', 'n', 'e'},
			wantKind: CBORMap,
			check: func(t *testing.T, v CBORValue) {
				got, ok := v.FindMap("fmt")
				if !ok || got.Str != "none" {
					t.Errorf("missing fmt=none: %+v", v.Map)
				}
			},
		},
		{
			name:     "bool true",
			bytes:    []byte{0xf5},
			wantKind: CBORBool,
			check:    func(t *testing.T, v CBORValue) { mustEq(t, v.Bool, true) },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, n, err := DecodeCBOR(c.bytes)
			if err != nil {
				t.Fatal(err)
			}
			if n != len(c.bytes) {
				t.Errorf("read %d bytes, expected %d", n, len(c.bytes))
			}
			if v.Kind != c.wantKind {
				t.Errorf("kind=%d want=%d", v.Kind, c.wantKind)
			}
			c.check(t, v)
		})
	}
}

func TestCBORFindMapInt(t *testing.T) {
	// {1: 2, 3: -7}  — minimal COSE-style payload
	data := []byte{0xa2, 0x01, 0x02, 0x03, 0x26}
	v, _, err := DecodeCBOR(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.FindMapInt(1); !ok || got.Int != 2 {
		t.Errorf("kty: %+v", got)
	}
	if got, ok := v.FindMapInt(3); !ok || got.Int != -7 {
		t.Errorf("alg: %+v", got)
	}
	if _, ok := v.FindMapInt(99); ok {
		t.Error("absent key should miss")
	}
}

func TestCBORRejectsIndefinite(t *testing.T) {
	// 0x9f = indefinite-length array — not supported.
	if _, _, err := DecodeCBOR([]byte{0x9f, 0xff}); err == nil {
		t.Error("expected error for indefinite-length input")
	}
}

func TestCBORRejectsTruncated(t *testing.T) {
	if _, _, err := DecodeCBOR([]byte{0x42, 'a'}); err == nil { // bytes(2) but only 1 byte
		t.Error("expected truncation error")
	}
}

func mustEq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
