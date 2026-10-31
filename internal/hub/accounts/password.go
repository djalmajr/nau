// Package accounts implements the hub accounts: the initial owner
// from a one-time environment token, the owner-issued invites, the
// e-mail/password login and the signed session cookies. The storage is
// the AccountStore interface (one SQL implementation and one in-memory
// one for the tests); the time and the randomness come from the
// configuration, so the tests are deterministic. No secret reaches a
// log, an error or a response: the password hashes, the session key and
// the bootstrap token are internal values only.
package accounts

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// The Argon2id parameters of the decision: 19456 KiB of memory, 2
// iterations, 1 thread, 16 bytes of salt and a 32-byte hash, in the PHC
// format with unpadded base64.
const (
	argon2MemoryKiB  = 19456
	argon2Iterations = 2
	argon2Threads    = 1
	argon2Version    = 19
	saltSize         = 16
	hashSize         = 32

	// The password policy: at least 12 characters and at most 128
	// bytes; above the bound the password is refused before hashing.
	minPasswordChars = 12
	maxPasswordBytes = 128

	phcPrefix = "$argon2id$"
)

// hashArgon2 derives one key with the fixed parameters.
func hashArgon2(password, salt []byte) []byte {
	return argon2.IDKey(password, salt, argon2Iterations, argon2MemoryKiB, argon2Threads, hashSize)
}

// b64 is the unpadded base64 alphabet of the PHC format.
var b64 = base64.RawStdEncoding

// HashPassword hashes one password with a fresh random salt and returns
// the PHC string. rand is the randomness source; a failing rand is an
// error, never a zero salt.
func HashPassword(rand io.Reader, password string) (string, error) {
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand, salt); err != nil {
		return "", fmt.Errorf("accounts: read the salt: %w", err)
	}
	dk := hashArgon2([]byte(password), salt)
	return fmt.Sprintf("%sv=%d$m=%d,t=%d,p=%d$%s$%s",
		phcPrefix, argon2Version, argon2MemoryKiB, argon2Iterations, argon2Threads,
		b64.EncodeToString(salt), b64.EncodeToString(dk)), nil
}

// parsePHC reads the parameters out of one PHC string. It refuses
// anything that is not the exact format: the argon2id type, the version,
// the m/t/p parameters and the two unpadded base64 fields.
func parsePHC(phc string) (salt, hash []byte, m, t uint32, p uint8, err error) {
	fields := strings.Split(phc, "$")
	// $argon2id$v=19$m=...,t=...,p=...$salt$hash -> 6 fields, the
	// first (before the leading $) is empty.
	if len(fields) != 6 || fields[0] != "" || fields[1] != "argon2id" {
		return nil, nil, 0, 0, 0, fmt.Errorf("accounts: not an argon2id PHC string")
	}
	var version uint32
	if _, err := fmt.Sscanf(fields[2], "v=%d", &version); err != nil || version != argon2Version {
		return nil, nil, 0, 0, 0, fmt.Errorf("accounts: unsupported PHC version")
	}
	for _, param := range strings.Split(fields[3], ",") {
		kv := strings.SplitN(param, "=", 2)
		if len(kv) != 2 {
			return nil, nil, 0, 0, 0, fmt.Errorf("accounts: bad PHC parameter %q", param)
		}
		v, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return nil, nil, 0, 0, 0, fmt.Errorf("accounts: bad PHC parameter %q", param)
		}
		switch kv[0] {
		case "m":
			m = uint32(v)
		case "t":
			t = uint32(v)
		case "p":
			p = uint8(v)
		default:
			return nil, nil, 0, 0, 0, fmt.Errorf("accounts: unknown PHC parameter %q", kv[0])
		}
	}
	if m == 0 || t == 0 || p == 0 {
		return nil, nil, 0, 0, 0, fmt.Errorf("accounts: incomplete PHC parameters")
	}
	salt, err = b64.DecodeString(fields[4])
	if err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("accounts: bad PHC salt")
	}
	hash, err = b64.DecodeString(fields[5])
	if err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("accounts: bad PHC hash")
	}
	return salt, hash, m, t, p, nil
}

// VerifyPassword checks one password against a PHC string. It reads the
// parameters from the string itself, so a hash produced with different
// parameters still verifies. A malformed string is simply a mismatch,
// never a panic or an error to leak.
func VerifyPassword(password, phc string) bool {
	salt, hash, m, t, p, err := parsePHC(phc)
	if err != nil || len(salt) == 0 || len(hash) == 0 {
		return false
	}
	dk := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(hash)))
	return subtle.ConstantTimeCompare(dk, hash) == 1
}

// PolicyOK is the size policy of the decision: at least 12 characters
// and at most 128 bytes.
func PolicyOK(password string) bool {
	return utf8.RuneCountInString(password) >= minPasswordChars && len(password) <= maxPasswordBytes
}

// The dummy hash equalizes the verification time of an unknown e-mail:
// it verifies a fixed password against a fixed salt with the same
// parameters as the real hashes, so the clock is the same.
var (
	dummyOnce sync.Once
	dummyHash string
)

func dummy() string {
	dummyOnce.Do(func() {
		salt := make([]byte, saltSize)
		dk := hashArgon2([]byte("nau-dummy-verification-password"), salt)
		dummyHash = fmt.Sprintf("%sv=%d$m=%d,t=%d,p=%d$%s$%s",
			phcPrefix, argon2Version, argon2MemoryKiB, argon2Iterations, argon2Threads,
			b64.EncodeToString(salt), b64.EncodeToString(dk))
	})
	return dummyHash
}

// sha256Hex is one digest as lowercase hex.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(sum)*2)
	for i, b := range sum {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out)
}
