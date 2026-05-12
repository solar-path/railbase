package password

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	hash, err := Hash("hunter2")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("PHC prefix wrong: %s", hash)
	}
	if err := Verify("hunter2", hash); err != nil {
		t.Errorf("Verify with correct password: %v", err)
	}
	if err := Verify("wrong", hash); err == nil {
		t.Errorf("Verify should fail for wrong password")
	}
}

func TestUniqueSalts(t *testing.T) {
	a, _ := Hash("same-pw")
	b, _ := Hash("same-pw")
	if a == b {
		t.Errorf("two hashes of same password should differ (random salt): %s", a)
	}
}

func TestParse_Malformed(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-phc",
		"$bcrypt$v=19$m=65536,t=3,p=4$abc$def",
		"$argon2id$v=18$m=65536,t=3,p=4$abc$def", // wrong version
		"$argon2id$v=19$m=bad,t=3,p=4$abc$def",
		"$argon2id$v=19$m=65536,t=3,p=4$bad-salt$def",
		"$argon2id$v=19$m=65536,t=3,p=4$YWJj$bad-tag",
	} {
		if err := Verify("anything", bad); err == nil {
			t.Errorf("Verify(%q) should error", bad)
		}
	}
}
