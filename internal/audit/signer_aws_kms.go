//go:build aws

package audit

// AWS KMS implementation of Signer. Compiled in with `-tags aws`.
//
// Why a build tag: same rationale as archive_target_s3.go — the AWS
// SDK is heavy and most deployments don't need it. Operators who
// require an HSM-resident seal key build with `-tags aws`, pin the
// KeyID, and Railbase's Sealer transparently uses the KMS Sign API
// for every seal-cron tick.
//
// Pre-flight on construction:
//   1. DescribeKey — confirm KeySpec=ECC_NIST_P256? No — Ed25519 keys
//      have KeySpec=ECC_NIST_P256 is WRONG; the correct constant is
//      KeySpec=ECC_NIST_P256 for ECDSA P-256. Ed25519 is not yet a
//      KMS KeySpec, BUT we accept the KMS configuration as-is and
//      surface any algorithm mismatch at first Sign call (KMS returns
//      InvalidKeyUsageException). This is intentional — letting the
//      operator's KMS team manage key spec via IAM is cleaner than
//      having Railbase second-guess.
//
//      (When AWS KMS does ship Ed25519 KeySpec we tighten the check.
//      For now any "asymmetric sign" key that accepts EDDSA works.)
//
//   2. GetPublicKey — fetch the public key bytes once, cache. The
//      cached key is what every seal row carries forward, so we
//      know the verifier can always reproduce ed25519.Verify without
//      another KMS round-trip.
//
// The cached public key is in SubjectPublicKeyInfo (SPKI) DER form
// from KMS. For Ed25519 SPKI = 12-byte ASN.1 prefix + 32 raw key
// bytes — we strip the prefix to get the 32-byte ed25519.PublicKey
// the chain stores.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// kmsSigner implements Signer via AWS KMS Sign API.
type kmsSigner struct {
	cfg    KMSSignerConfig
	client *kms.Client
	pub    ed25519.PublicKey
}

// init wires the build-tag-aware constructor.
func init() {
	newKMSSignerImpl = func(cfg KMSSignerConfig) (Signer, error) {
		awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
			awsconfig.WithRegion(cfg.Region),
		)
		if err != nil {
			return nil, fmt.Errorf("audit: kms signer: load AWS config: %w", err)
		}
		opts := []func(*kms.Options){}
		if cfg.EndpointURL != "" {
			ep := cfg.EndpointURL
			opts = append(opts, func(o *kms.Options) {
				o.BaseEndpoint = aws.String(ep)
			})
		}
		client := kms.NewFromConfig(awsCfg, opts...)

		// Fetch the public key once. KMS returns it in SubjectPublicKeyInfo
		// DER format; for Ed25519 the wrapper is a known 12-byte ASN.1
		// prefix followed by the 32 raw key bytes.
		out, err := client.GetPublicKey(context.Background(), &kms.GetPublicKeyInput{
			KeyId: aws.String(cfg.KeyID),
		})
		if err != nil {
			return nil, fmt.Errorf("audit: kms signer: GetPublicKey: %w", err)
		}
		pub, err := extractEd25519FromSPKI(out.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("audit: kms signer: extract Ed25519 from SPKI: %w", err)
		}
		return &kmsSigner{cfg: cfg, client: client, pub: pub}, nil
	}
}

func (s *kmsSigner) PublicKey() ed25519.PublicKey { return s.pub }

// Sign asks KMS to produce an Ed25519 signature over msg. Pass-through
// — no client-side hashing (Ed25519 hashes internally). Returns the
// raw 64-byte signature bytes.
//
// Algorithm: AWS KMS exposes Ed25519 as SigningAlgorithmSpec="EDDSA"
// (added 2024). Since the SDK enum may not be available across all
// versions, we use the string form via the underlying type — works
// regardless of when the typed constant lands in aws-sdk-go-v2.
func (s *kmsSigner) Sign(msg []byte) ([]byte, error) {
	out, err := s.client.Sign(context.Background(), &kms.SignInput{
		KeyId:            aws.String(s.cfg.KeyID),
		Message:          msg,
		MessageType:      kmstypes.MessageTypeRaw,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpec("EDDSA"),
	})
	if err != nil {
		return nil, fmt.Errorf("audit: kms signer: Sign: %w", err)
	}
	if len(out.Signature) != ed25519.SignatureSize {
		// KMS may pad/wrap the signature for non-Ed25519 algorithms. We
		// only support pure Ed25519 — refuse anything else so the
		// verifier doesn't later silently fail.
		return nil, fmt.Errorf("audit: kms signer: signature size %d (want %d) — key is not Ed25519",
			len(out.Signature), ed25519.SignatureSize)
	}
	return out.Signature, nil
}

// extractEd25519FromSPKI extracts the raw 32-byte Ed25519 public key
// from a SubjectPublicKeyInfo (SPKI) DER blob. AWS KMS returns SPKI
// for asymmetric keys via GetPublicKey.
//
// Ed25519 SPKI structure (RFC 8410):
//
//	SEQUENCE {
//	  SEQUENCE { OID 1.3.101.112 }         -- algorithm
//	  BIT STRING { 32-byte raw pubkey }    -- subjectPublicKey
//	}
//
// The full DER is 44 bytes: 12-byte prefix + 32 raw key bytes. We
// don't fully parse the ASN.1 — the prefix is stable and shorter to
// pattern-match than to depend on encoding/asn1. If KMS ever adjusts
// the wrapper the assertion below trips and the operator sees an
// explicit error.
func extractEd25519FromSPKI(spki []byte) (ed25519.PublicKey, error) {
	if len(spki) != 44 {
		return nil, fmt.Errorf("unexpected SPKI length %d (want 44 for Ed25519)", len(spki))
	}
	// The 12-byte fixed prefix for Ed25519 SPKI in DER:
	//   30 2a 30 05 06 03 2b 65 70 03 21 00
	expectedPrefix := []byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00}
	for i, b := range expectedPrefix {
		if spki[i] != b {
			return nil, errors.New("SPKI prefix mismatch — key is not Ed25519")
		}
	}
	return ed25519.PublicKey(spki[12:]), nil
}
