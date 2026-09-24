package crypto

import (
	crand "crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// keyWords is the wordlist for auto-generated session passphrases.
var keyWords = []string{
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot",
	"golf", "hotel", "india", "juliet", "kilo", "lima",
	"mike", "november", "oscar", "papa", "quebec", "romeo",
	"sierra", "tango", "uniform", "victor", "whiskey", "xray",
	"yankee", "zulu", "niner", "zero", "one", "two",
	"three", "four", "five", "six", "seven", "eight",
}

// GeneratePassphrase generates a random 6-word passphrase using crypto/rand.
func GeneratePassphrase() (string, error) {
	words := make([]string, 6)
	for i := range words {
		idx, err := crand.Int(crand.Reader, big.NewInt(int64(len(keyWords))))
		if err != nil {
			return "", fmt.Errorf("crypto/rand failed: %w", err)
		}
		words[i] = keyWords[idx.Int64()]
	}
	return strings.Join(words, "-"), nil
}
