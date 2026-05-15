package audit

// Phase 4 — pluggable signer for the Ed25519 audit seal chain.
//
// The default Sealer reads the raw private key from disk
// (`<dataDir>/.audit_seal_key`, chmod 0600). That posture is fine for
// self-hosted deployments: the operator controls the host, the key is
// on the same filesystem as the audit data, threat model is "someone
// roots the box and rewrites history" (and the key being on the box
// means a rooted box can re-sign tampered chains — same trust
// boundary as a self-signed root CA).
//
// Regulated deployments need a tighter posture: the private key must
// LIVE in an HSM / cloud KMS and NEVER leave the secure boundary.
// AWS KMS, GCP Cloud KMS, and Azure Key Vault all expose a Sign API
// that accepts a message + algorithm name and returns the signature
// — the private key bytes are not exportable. This file abstracts
// over those: Sealer holds a Signer interface, with the local-file
// implementation being one concrete option and aws-kms being another.
//
// What we DON'T abstract: the Verify side. ed25519.Verify is purely
// public-key + signature + message arithmetic and stays in pure-Go.
// Operators verify chains by hex-encoding the public_key column +
// running `ed25519.Verify` regardless of which signer produced the
// seal — this keeps the verification primitive independent of any
// vendor SDK.
//
// What goes into a seal's `public_key` column:
//   - Local signer: the 32-byte ed25519.PublicKey derived from the
//     stored private key.
//   - KMS signer: the 32-byte ed25519.PublicKey fetched from the KMS
//     at construction time (via GetPublicKey). The KMS holds the
//     matching private side internally.
//
// Algorithm constraint: Ed25519 only. AWS KMS supports Ed25519 (as of
// 2024); GCP Cloud KMS does NOT (only ECDSA + RSA). When we add a GCP
// implementation it'll have to live behind an ed25519-via-RSA-PSS
// adapter or the operator picks ECDSA (chain version bump). Out of
// Phase 4 scope.

import (
	"crypto/ed25519"
	"errors"
)

// Signer is the seal-time signing primitive. Implementations:
//
//   * localSigner (default): wraps a local ed25519.PrivateKey loaded
//     from disk. Backwards-compatible with the pre-Phase-4 Sealer.
//
//   * kmsSigner (opt-in, build tag `aws`): calls AWS KMS Sign with
//     SigningAlgorithmEdDSA. The KMS key MUST be Ed25519 (KeySpec=
//     ECC_NIST_P256 etc. won't work — we constrain at construct time).
//
// Goroutine-safe: implementations must allow concurrent Sign calls
// (the local one is, by virtue of ed25519.Sign being stateless; the
// KMS one is by virtue of the AWS SDK client being concurrent).
type Signer interface {
	// Sign produces an Ed25519 signature over msg. Returns 64 bytes
	// on success.
	Sign(msg []byte) ([]byte, error)

	// PublicKey returns the matching Ed25519 public key. Captured
	// once at construct time; never round-trips back to the KMS.
	PublicKey() ed25519.PublicKey
}

// localSigner wraps a local ed25519.PrivateKey. Default signer for
// the bare-binary deployment.
type localSigner struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// newLocalSigner constructs a localSigner from a raw private key.
// Returns an error if the key is wrong size.
func newLocalSigner(priv ed25519.PrivateKey) (*localSigner, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("audit: local signer: wrong key size")
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("audit: local signer: cannot derive public key")
	}
	return &localSigner{priv: priv, pub: pub}, nil
}

func (s *localSigner) Sign(msg []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, msg), nil
}

func (s *localSigner) PublicKey() ed25519.PublicKey { return s.pub }

// ─────────────────────────────────────────────────────────────────
// KMS signer (opt-in, build-tagged).
// ─────────────────────────────────────────────────────────────────

// KMSSignerConfig configures an AWS KMS Ed25519 signer. The named key
// MUST be an Ed25519 key (KeySpec=ECC_NIST_P256 etc. is rejected).
// SigningAlgorithm is always EDDSA — no other algorithm matches our
// chain format.
type KMSSignerConfig struct {
	// KeyID is the AWS KMS key identifier: full ARN, key UUID, or
	// alias name. Required.
	KeyID string
	// Region is the AWS region of the key. Required.
	Region string
	// EndpointURL overrides the default KMS endpoint. Useful for
	// LocalStack in tests. Optional.
	EndpointURL string
}

// NewKMSSigner constructs a Signer backed by AWS KMS. Returns
// ErrNoKMSSupport when the binary was built without the AWS SDK
// (default build); operators who need KMS-signed seals build with
// `-tags aws`.
func NewKMSSigner(cfg KMSSignerConfig) (Signer, error) {
	if cfg.KeyID == "" {
		return nil, errors.New("audit: kms signer: KeyID required")
	}
	if cfg.Region == "" {
		return nil, errors.New("audit: kms signer: Region required")
	}
	return newKMSSignerImpl(cfg)
}

// newKMSSignerImpl is a build-tag seam. Default returns
// ErrNoKMSSupport; the `aws` build tag swaps in a real client.
var newKMSSignerImpl = func(_ KMSSignerConfig) (Signer, error) {
	return nil, ErrNoKMSSupport
}

// ErrNoKMSSupport is returned by NewKMSSigner when the binary was
// built without AWS SDK support. Operators see this once at
// config-load time, not on every seal run.
var ErrNoKMSSupport = errors.New("audit: kms signer: build does not include AWS SDK (rebuild with `-tags aws`)")
