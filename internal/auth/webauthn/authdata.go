package webauthn

import (
	"encoding/binary"
	"errors"
)

// authData is the parsed shape of authenticatorData per §6.1.
// Layout:
//
//	[0:32]   rpIdHash
//	[32]     flags (UP, RFU1, UV, BS, BE, AT, ED, ...)
//	[33:37]  signCount (big-endian uint32)
//	[37:53]  AAGUID         (only when AT flag set)
//	[53:55]  credIdLen      (only when AT flag set)
//	[55:...] credentialId   (credIdLen bytes)
//	         + COSE public key  (variable, CBOR-encoded)
//	[...]    extensions     (only when ED flag set)
//
// In assertions (no registration), AT is unset and we stop at byte 37.
type authData struct {
	RPIDHash           []byte
	Flags              authFlags
	SignCount          uint32
	AttestedCredential *attestedCredentialData
}

type authFlags byte

func (f authFlags) UserPresent() bool  { return f&0x01 != 0 } // UP
func (f authFlags) UserVerified() bool { return f&0x04 != 0 } // UV
func (f authFlags) AttestedData() bool { return f&0x40 != 0 } // AT
func (f authFlags) Extensions() bool   { return f&0x80 != 0 } // ED

type attestedCredentialData struct {
	AAGUID         []byte // 16 bytes
	CredentialID   []byte
	PublicKeyCOSE  []byte // raw CBOR slice — we don't decode here
}

// parseAuthData parses both registration and assertion authData
// payloads. Returns a fully populated authData; AttestedCredential
// is nil when the AT flag isn't set.
func parseAuthData(buf []byte) (*authData, error) {
	if len(buf) < 37 {
		return nil, errors.New("authData too short")
	}
	ad := &authData{
		RPIDHash:  buf[0:32],
		Flags:     authFlags(buf[32]),
		SignCount: binary.BigEndian.Uint32(buf[33:37]),
	}
	if !ad.Flags.AttestedData() {
		return ad, nil
	}
	if len(buf) < 55 {
		return nil, errors.New("authData truncated before attested-data")
	}
	credIdLen := int(binary.BigEndian.Uint16(buf[53:55]))
	if len(buf) < 55+credIdLen {
		return nil, errors.New("authData truncated in credential id")
	}
	rest := buf[55+credIdLen:]
	// The public key is CBOR-encoded; we need its byte slice but we
	// don't have to decode it here. DecodeCBOR returns (value,
	// bytesRead) — we use bytesRead to slice off the exact span.
	_, n, err := DecodeCBOR(rest)
	if err != nil {
		return nil, err
	}
	ad.AttestedCredential = &attestedCredentialData{
		AAGUID:        buf[37:53],
		CredentialID:  buf[55 : 55+credIdLen],
		PublicKeyCOSE: rest[:n],
	}
	return ad, nil
}
