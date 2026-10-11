package uploads

import (
	"strings"
	"testing"
)

func TestValidMediaType(t *testing.T) {
	for _, s := range []string{"image/png", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "text/plain;charset=utf-8"} {
		if !validMediaType(s) {
			t.Errorf("rejected %q", s)
		}
	}
	for _, s := range []string{"", "image", "/png", "image/", "image/png/x", "image/ png", "imäge/png", "a/" + strings.Repeat("b", 99)} {
		if validMediaType(s) {
			t.Errorf("accepted %q", s)
		}
	}
}
