package machines

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
)

// crockford is the Crockford base32 alphabet without the ambiguous
// letters (no I, L, O or U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// codeLen is the number of letters of a pairing code.
const codeLen = 8

// drawCode draws one random pairing code: eight letters of the
// Crockford alphabet. One byte per letter, 256 being a multiple of 32,
// so the modulo is without bias.
func (m *Service) drawCode() (string, error) {
	b := make([]byte, codeLen)
	if _, err := io.ReadFull(m.rand, b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = crockford[int(b[i])%len(crockford)]
	}
	return string(b), nil
}

// normalizeCode canonicalizes a code as the developer typed it:
// uppercased and without the hyphens. Anything else (a space, a letter
// outside the alphabet) is not a code and fails validCode.
func normalizeCode(s string) string {
	return strings.ReplaceAll(strings.ToUpper(s), "-", "")
}

// validCode reports whether the normalized code has the shape of a
// pairing code: exactly eight Crockford letters.
func validCode(s string) bool {
	if len(s) != codeLen {
		return false
	}
	for i := 0; i < codeLen; i++ {
		if strings.IndexByte(crockford, s[i]) < 0 {
			return false
		}
	}
	return true
}

// displayCode shows the code to the developer, as XXXX-XXXX.
func displayCode(s string) string {
	return s[:4] + "-" + s[4:]
}

// hashHex is the hex form of the SHA-256 of b: the stored form of the
// pairing code and the poll token, and of the machine key.
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
