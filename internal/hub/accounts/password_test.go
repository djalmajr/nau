package accounts

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestHashPasswordProducesThePHCFormat(t *testing.T) {
	phc, err := HashPassword(rand.Reader, "a-strong-enough-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
	if !strings.HasPrefix(phc, `$argon2id$v=19$m=19456,t=2,p=1$`) {
		t.Fatalf("the PHC prefix = %q", phc)
	}
	rest := phc[len(`$argon2id$v=19$m=19456,t=2,p=1`):]
	parts := strings.Split(rest, "$")
	if len(parts) != 3 || parts[0] != "" || parts[2] == "" {
		t.Fatalf("the PHC has %d parts", len(parts))
	}
	salt, err := b64.DecodeString(parts[1])
	if err != nil || len(salt) != saltSize {
		t.Fatalf("the salt = %d bytes (err %v)", len(salt), err)
	}
	hash, err := b64.DecodeString(parts[2])
	if err != nil || len(hash) != hashSize {
		t.Fatalf("the hash = %d bytes (err %v)", len(hash), err)
	}
	if !VerifyPassword("a-strong-enough-password", phc) {
		t.Fatal("the fresh hash does not verify its password")
	}
	if VerifyPassword("a-different-password", phc) {
		t.Fatal("a different password verifies the hash")
	}
}

func TestHashPasswordIsRandomInTheSalt(t *testing.T) {
	a, err := HashPassword(rand.Reader, "same-password-same")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	b, err := HashPassword(rand.Reader, "same-password-same")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are equal: the salt is not random")
	}
}

func TestVerifyPasswordReadsTheParametersFromTheHash(t *testing.T) {
	// A hash built with other parameters (a cheaper m) verifies with
	// its own parameters, proving the reader uses the string, not the
	// constants.
	salt := make([]byte, saltSize)
	dk := argon2.IDKey([]byte("param-read-test-pw"), salt, 3, 64, 1, 32)
	phc := `$argon2id$v=19$m=64,t=3,p=1$` + b64.EncodeToString(salt) + `$` + b64.EncodeToString(dk)
	if !VerifyPassword("param-read-test-pw", phc) {
		t.Fatal("the non-standard parameters do not verify")
	}
	if VerifyPassword("another-password-x", phc) {
		t.Fatal("a wrong password verifies under the read parameters")
	}
}

func TestVerifyPasswordRefusesMalformedStrings(t *testing.T) {
	good, err := HashPassword(rand.Reader, "a-strong-enough-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	for name, phc := range map[string]string{
		"empty":          "",
		"not phc":        "argon2id$garbage",
		"wrong type":     strings.Replace(good, "argon2id", "argon2i", 1),
		"wrong version":  strings.Replace(good, "v=19", "v=16", 1),
		"missing hash":   good[:len(good)-4] + "!!",
		"bad salt":       strings.Replace(good, b64SaltOf(good), "!!!!", 1),
		"extra field":    good + "$x",
		"float param":    strings.Replace(good, "t=2", "t=2.5", 1),
		"negative param": strings.Replace(good, "t=2", "t=-2", 1),
	} {
		if VerifyPassword("a-strong-enough-password", phc) {
			t.Fatalf("%s: the malformed string verifies", name)
		}
	}
}

// b64SaltOf pulls the salt field out of a PHC string.
func b64SaltOf(phc string) string {
	parts := strings.Split(phc, "$")
	return parts[4]
}

func TestPolicyBounds(t *testing.T) {
	if PolicyOK(strings.Repeat("a", 11)) {
		t.Fatal("11 characters pass the policy")
	}
	if !PolicyOK(strings.Repeat("a", 12)) {
		t.Fatal("12 characters fail the policy")
	}
	if PolicyOK(strings.Repeat("a", maxPasswordBytes+1)) {
		t.Fatal("129 bytes pass the policy")
	}
	if !PolicyOK(strings.Repeat("a", maxPasswordBytes)) {
		t.Fatal("128 bytes fail the policy")
	}
	// 12 multi-byte characters are 12 characters and 24 bytes: inside.
	if !PolicyOK(strings.Repeat("ç", 12)) {
		t.Fatal("12 multi-byte characters fail the policy")
	}
}

func TestDummyHashHasTheSameConfiguration(t *testing.T) {
	phc := dummy()
	salt, hash, m, it, p, err := parsePHC(phc)
	if err != nil {
		t.Fatalf("the dummy is not a PHC string: %v", err)
	}
	if m != argon2MemoryKiB || it != argon2Iterations || p != argon2Threads || len(salt) != saltSize || len(hash) != hashSize {
		t.Fatalf("the dummy configuration = m %d t %d p %d salt %d hash %d", m, it, p, len(salt), len(hash))
	}
	if VerifyPassword("some-login-attempt", phc) {
		t.Fatal("the dummy verifies an arbitrary password")
	}
}

func TestHashPasswordRandFailureIsAnError(t *testing.T) {
	if _, err := HashPassword(bytes.NewReader(nil), "a-strong-enough-password"); err == nil {
		t.Fatal("a failing rand is not an error")
	}
}

func BenchmarkHashPassword(b *testing.B) {
	pw := "benchmark-password-1"
	for i := 0; i < b.N; i++ {
		if _, err := HashPassword(rand.Reader, pw); err != nil {
			b.Fatal(err)
		}
	}
}
