package ids

import (
	"bytes"
	"testing"
)

func TestRandomFitsIn63Bits(t *testing.T) {
	for range 100 {
		if id := Random(); id == 0 || id>>63 != 0 {
			t.Fatalf("id %d is zero or uses the top bit", id)
		}
	}
}

func TestNewIsVersion4(t *testing.T) {
	u := New()
	if u[6]>>4 != 4 || u[8]>>6 != 2 {
		t.Fatalf("not a version 4 UUID: %s", u)
	}
	if u == New() {
		t.Fatal("two ids are equal")
	}
}

func TestParse(t *testing.T) {
	u := New()
	got, ok := Parse(u.Bytes())
	if !ok || got != u {
		t.Fatalf("round trip gave %s, %v", got, ok)
	}
	for _, b := range [][]byte{nil, make([]byte, 15), make([]byte, 17)} {
		if _, ok := Parse(b); ok {
			t.Fatalf("parsed %d bytes", len(b))
		}
	}
}

func TestHex(t *testing.T) {
	u := UUID{0x00, 0x01, 0xab, 0xff}
	if s := u.String(); s != "0001abff000000000000000000000000" {
		t.Fatalf("hex %q", s)
	}
	if got, ok := FromHex(u.String()); !ok || got != u {
		t.Fatalf("from hex gave %s, %v", got, ok)
	}
	if _, ok := FromHex("0001ABFF000000000000000000000000"); ok {
		t.Fatal("parsed uppercase hex")
	}
}

func TestParticipantsOrderBytewise(t *testing.T) {
	a, b := UUID{0x7f}, UUID{0x80}
	if x, y := Participants(b, a); x != a || y != b {
		t.Fatal("not ordered")
	}
	if x, y := Participants(a, b); bytes.Compare(x[:], y[:]) >= 0 {
		t.Fatal("not ordered")
	}
}
