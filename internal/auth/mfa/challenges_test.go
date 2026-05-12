package mfa

import (
	"testing"
)

// mfa.Challenge.Complete is pure-Go logic — easy to cover without a
// database. The Store paths (Create/Lookup/Solve) need Postgres, so
// they're exercised end-to-end in the api/auth integration test.

func TestComplete_AllSolved(t *testing.T) {
	c := &Challenge{
		FactorsRequired: []Factor{FactorPassword, FactorTOTP},
		FactorsSolved:   []Factor{FactorPassword, FactorTOTP},
	}
	if !c.Complete() {
		t.Errorf("complete should be true when all factors solved")
	}
}

func TestComplete_Missing(t *testing.T) {
	c := &Challenge{
		FactorsRequired: []Factor{FactorPassword, FactorTOTP},
		FactorsSolved:   []Factor{FactorPassword},
	}
	if c.Complete() {
		t.Errorf("complete should be false when totp missing")
	}
}

func TestComplete_RecoverySatisfiesTOTP(t *testing.T) {
	// A recovery-code solve fills the TOTP slot.
	c := &Challenge{
		FactorsRequired: []Factor{FactorPassword, FactorTOTP},
		FactorsSolved:   []Factor{FactorPassword, FactorRecovery},
	}
	if !c.Complete() {
		t.Errorf("recovery solve should fill the TOTP requirement")
	}
}

func TestComplete_ExtraSolvedFactorsIgnored(t *testing.T) {
	c := &Challenge{
		FactorsRequired: []Factor{FactorPassword},
		FactorsSolved:   []Factor{FactorPassword, FactorTOTP, FactorEmailOTP},
	}
	if !c.Complete() {
		t.Errorf("extra solved factors should not break completion")
	}
}

func TestComplete_NilSafe(t *testing.T) {
	var c *Challenge
	if c.Complete() {
		t.Errorf("nil challenge should not be complete")
	}
}

func TestFactorsSorted(t *testing.T) {
	got := factorsSorted([]Factor{FactorTOTP, FactorPassword, FactorEmailOTP})
	want := []Factor{FactorEmailOTP, FactorPassword, FactorTOTP}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: %v vs %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}
