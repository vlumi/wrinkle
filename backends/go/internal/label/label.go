// Package label checks short texts people name things with: user names and
// group topics.
package label

import (
	"unicode"
	"unicode/utf8"
)

// Valid reports whether s is valid UTF-8 of 1 to maxBytes bytes, without
// control characters or white space at either end.
func Valid(s string, maxBytes int) bool {
	if s == "" || len(s) > maxBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.Is(unicode.Cc, r) {
			return false
		}
	}
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	return !unicode.Is(unicode.White_Space, first) && !unicode.Is(unicode.White_Space, last)
}
