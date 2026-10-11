package e2ee

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"slices"
	"strconv"
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// go test ./e2ee -update rewrites testdata/vectors.json from the inputs
// below; the web and iOS clients check the same file's values.
var update = flag.Bool("update", false, "rewrite testdata/vectors.json")

const vectorsPath = "testdata/vectors.json"

type vectorDevice struct {
	UserID     string `json:"user_id"`
	DeviceID   string `json:"device_id"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

type vectors struct {
	Description string `json:"description"`
	RFC7748     struct {
		AlicePrivate string `json:"alice_private"`
		AlicePublic  string `json:"alice_public"`
		BobPrivate   string `json:"bob_private"`
		BobPublic    string `json:"bob_public"`
		Shared       string `json:"shared"`
	} `json:"rfc7748"`
	RFC5869 struct {
		IKM  string `json:"ikm"`
		Salt string `json:"salt"`
		Info string `json:"info"`
		L    int    `json:"length"`
		OKM  string `json:"okm"`
	} `json:"rfc5869_case1"`
	Devices map[string]vectorDevice `json:"devices"`
	Wrap    struct {
		Sender         string `json:"sender"`
		Recipient      string `json:"recipient"`
		ConversationID string `json:"conversation_id"`
		Epoch          uint32 `json:"epoch"`
		Key            string `json:"key"`
		Nonce          string `json:"nonce"`
		SharedSecret   string `json:"shared_secret"`
		Info           string `json:"info"`
		WrappingKey    string `json:"wrapping_key"`
		Wrapped        string `json:"wrapped"`
	} `json:"wrap"`
	Seal struct {
		Key             string `json:"key"`
		Nonce           string `json:"nonce"`
		ConversationID  string `json:"conversation_id"`
		Epoch           uint32 `json:"epoch"`
		SenderUserID    string `json:"sender_user_id"`
		ClientMessageID string `json:"client_message_id"`
		Text            string `json:"text"`
		Plaintext       string `json:"plaintext"`
		AAD             string `json:"aad"`
		Ciphertext      string `json:"ciphertext"`
	} `json:"seal"`
	Reaction struct {
		Key            string   `json:"key"`
		Nonce          string   `json:"nonce"`
		ConversationID string   `json:"conversation_id"`
		Epoch          uint32   `json:"epoch"`
		UserID         string   `json:"user_id"`
		Seq            uint64   `json:"seq"`
		Emoji          []string `json:"emoji"`
		Plaintext      string   `json:"plaintext"`
		AAD            string   `json:"aad"`
		Ciphertext     string   `json:"ciphertext"`
	} `json:"reaction"`
	Blob struct {
		Key       string `json:"key"`
		Nonce     string `json:"nonce"`
		Plaintext string `json:"plaintext"`
		AAD       string `json:"aad"`
		Blob      string `json:"blob"`
		Digest    string `json:"digest"`
	} `json:"blob"`
	Fingerprints struct {
		Users []struct {
			UserID      string   `json:"user_id"`
			Devices     []string `json:"devices"`
			Fingerprint string   `json:"fingerprint"`
		} `json:"users"`
		SafetyNumber string `json:"safety_number"`
	} `json:"fingerprints"`
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

func seq(start byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

func h(b []byte) string { return hex.EncodeToString(b) }

// inputs fills in the fixed inputs of the vectors.
func inputs() *vectors {
	v := &vectors{Description: "Test vectors for content.proto's cryptography. Ids and keys are hex, device ids decimal u64 strings; device private keys are X25519 scalars as RFC 7748 takes them."}
	v.RFC7748.AlicePrivate = "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"
	v.RFC7748.AlicePublic = "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"
	v.RFC7748.BobPrivate = "5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb"
	v.RFC7748.BobPublic = "de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f"
	v.RFC7748.Shared = "4a5d9d5ba4ce2de1728e3bf480350f25e07e21c947d19e3376f09b3c1e161742"
	v.RFC5869.IKM = "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"
	v.RFC5869.Salt = "000102030405060708090a0b0c"
	v.RFC5869.Info = "f0f1f2f3f4f5f6f7f8f9"
	v.RFC5869.L = 42
	v.RFC5869.OKM = "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865"

	userA := h(seq(0xa0, 16))
	userB := h(seq(0xb0, 16))
	v.Devices = map[string]vectorDevice{
		// Device ids above 2^53 and with the high bit set, so encodings
		// that lose precision or sign show.
		"a1": {UserID: userA, DeviceID: "9223372036854775809", PrivateKey: h(seq(0x01, 32))},
		"a2": {UserID: userA, DeviceID: "81985529216486895", PrivateKey: h(seq(0x21, 32))},
		"b1": {UserID: userB, DeviceID: "18364758544493064720", PrivateKey: h(seq(0x41, 32))},
	}
	v.Wrap.Sender = "a1"
	v.Wrap.Recipient = "b1"
	v.Wrap.ConversationID = h(seq(0xc0, 16))
	v.Wrap.Epoch = 1
	v.Wrap.Key = h(seq(0x80, 32))
	v.Wrap.Nonce = h(seq(0x10, 12))
	v.Seal.Key = v.Wrap.Key
	v.Seal.Nonce = h(seq(0x30, 12))
	v.Seal.ConversationID = v.Wrap.ConversationID
	v.Seal.Epoch = 1
	v.Seal.SenderUserID = userA
	v.Seal.ClientMessageID = "client-message-1"
	v.Seal.Text = "hello"
	v.Reaction.Key = v.Wrap.Key
	v.Reaction.Nonce = h(seq(0x50, 12))
	v.Reaction.ConversationID = v.Wrap.ConversationID
	v.Reaction.Epoch = 2
	v.Reaction.UserID = userB
	// Above 2^32, so 32-bit encodings show; below 2^53 for JSON.
	v.Reaction.Seq = 1<<32 + 2
	// One code point, one with a variation selector, one ZWJ sequence.
	v.Reaction.Emoji = []string{"👍", "❤️", "👩‍👩‍👧"}
	v.Blob.Key = h(seq(0x60, 32))
	v.Blob.Nonce = h(seq(0x70, 12))
	// More than two AES blocks, not a multiple of one.
	v.Blob.Plaintext = h(seq(0x00, 40))
	v.Fingerprints.Users = make([]struct {
		UserID      string   `json:"user_id"`
		Devices     []string `json:"devices"`
		Fingerprint string   `json:"fingerprint"`
	}, 2)
	// Listed out of device id order; the fingerprint sorts them.
	v.Fingerprints.Users[0].UserID = userA
	v.Fingerprints.Users[0].Devices = []string{"a1", "a2"}
	v.Fingerprints.Users[1].UserID = userB
	v.Fingerprints.Users[1].Devices = []string{"b1"}
	return v
}

func deviceID(t *testing.T, d vectorDevice) uint64 {
	t.Helper()
	id, err := strconv.ParseUint(d.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func reactionContext(t *testing.T, v *vectors) ReactionContext {
	t.Helper()
	return ReactionContext{
		ConversationID: unhex(t, v.Reaction.ConversationID),
		Epoch:          v.Reaction.Epoch,
		UserID:         unhex(t, v.Reaction.UserID),
		Seq:            v.Reaction.Seq,
	}
}

// compute fills in every expected value from the inputs.
func compute(t *testing.T, v *vectors) {
	t.Helper()
	for name, d := range v.Devices {
		priv, err := PrivateKey(unhex(t, d.PrivateKey))
		if err != nil {
			t.Fatal(err)
		}
		d.PublicKey = h(priv.PublicKey().Bytes())
		v.Devices[name] = d
	}

	s, r := v.Devices[v.Wrap.Sender], v.Devices[v.Wrap.Recipient]
	sPriv, _ := PrivateKey(unhex(t, s.PrivateKey))
	w := WrapContext{
		ConversationID:    unhex(t, v.Wrap.ConversationID),
		Epoch:             v.Wrap.Epoch,
		SenderUserID:      unhex(t, s.UserID),
		SenderDeviceID:    deviceID(t, s),
		RecipientUserID:   unhex(t, r.UserID),
		RecipientDeviceID: deviceID(t, r),
	}
	secret, err := SharedSecret(sPriv, unhex(t, r.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	wk, err := HKDF(secret, w.Info())
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := Wrap(sPriv, unhex(t, r.PublicKey), w, unhex(t, v.Wrap.Nonce), unhex(t, v.Wrap.Key))
	if err != nil {
		t.Fatal(err)
	}
	v.Wrap.SharedSecret, v.Wrap.Info, v.Wrap.WrappingKey, v.Wrap.Wrapped = h(secret), h(w.Info()), h(wk), h(wrapped)

	c := ContentContext{
		ConversationID:  unhex(t, v.Seal.ConversationID),
		Epoch:           v.Seal.Epoch,
		SenderUserID:    unhex(t, v.Seal.SenderUserID),
		ClientMessageID: v.Seal.ClientMessageID,
	}
	plaintext, err := MarshalContent(&pb.Content{Body: &pb.Content_Text{Text: v.Seal.Text}})
	if err != nil {
		t.Fatal(err)
	}
	ct, err := Seal(unhex(t, v.Seal.Key), unhex(t, v.Seal.Nonce), plaintext, c)
	if err != nil {
		t.Fatal(err)
	}
	v.Seal.Plaintext, v.Seal.AAD, v.Seal.Ciphertext = h(plaintext), h(c.AAD()), h(ct)

	rc := reactionContext(t, v)
	plaintext, err = MarshalReactions(&pb.Reactions{Emoji: v.Reaction.Emoji})
	if err != nil {
		t.Fatal(err)
	}
	ct, err = Seal(unhex(t, v.Reaction.Key), unhex(t, v.Reaction.Nonce), plaintext, rc)
	if err != nil {
		t.Fatal(err)
	}
	v.Reaction.Plaintext, v.Reaction.AAD, v.Reaction.Ciphertext = h(plaintext), h(rc.AAD()), h(ct)

	blob, err := SealBlob(unhex(t, v.Blob.Key), unhex(t, v.Blob.Nonce), unhex(t, v.Blob.Plaintext))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(blob)
	v.Blob.AAD, v.Blob.Blob, v.Blob.Digest = h([]byte(blobLabel)), h(blob), h(digest[:])

	var fps [][]byte
	for i, u := range v.Fingerprints.Users {
		var keys []DeviceKey
		for _, name := range u.Devices {
			d := v.Devices[name]
			keys = append(keys, DeviceKey{DeviceID: deviceID(t, d), PublicKey: unhex(t, d.PublicKey)})
		}
		fp := Fingerprint(unhex(t, u.UserID), keys)
		v.Fingerprints.Users[i].Fingerprint = h(fp)
		fps = append(fps, fp)
	}
	v.Fingerprints.SafetyNumber = SafetyNumber(fps[0], fps[1])
}

func TestVectors(t *testing.T) {
	want := inputs()
	compute(t, want)
	if *update {
		b, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vectorsPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var got vectors
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if !bytes.Equal(gb, wb) {
		t.Fatalf("%s differs from the computed vectors:\n%s\nwant\n%s", vectorsPath, gb, wb)
	}

	// The file's expected values also open with the package.
	r := got.Devices[got.Wrap.Recipient]
	s := got.Devices[got.Wrap.Sender]
	rPriv, _ := PrivateKey(unhex(t, r.PrivateKey))
	k, err := Unwrap(rPriv, unhex(t, s.PublicKey), WrapContext{
		ConversationID:    unhex(t, got.Wrap.ConversationID),
		Epoch:             got.Wrap.Epoch,
		SenderUserID:      unhex(t, s.UserID),
		SenderDeviceID:    deviceID(t, s),
		RecipientUserID:   unhex(t, r.UserID),
		RecipientDeviceID: deviceID(t, r),
	}, unhex(t, got.Wrap.Wrapped))
	if err != nil || h(k) != got.Wrap.Key {
		t.Fatalf("unwrap: %x, %v", k, err)
	}
	c := ContentContext{
		ConversationID:  unhex(t, got.Seal.ConversationID),
		Epoch:           got.Seal.Epoch,
		SenderUserID:    unhex(t, got.Seal.SenderUserID),
		ClientMessageID: got.Seal.ClientMessageID,
	}
	content, err := OpenContent(unhex(t, got.Seal.Key), c, &pb.Sealed{
		Epoch: got.Seal.Epoch, Nonce: unhex(t, got.Seal.Nonce), Ciphertext: unhex(t, got.Seal.Ciphertext),
	})
	if err != nil || content.GetText() != got.Seal.Text {
		t.Fatalf("open: %v, %v", content, err)
	}
	reactions, err := OpenReactions(unhex(t, got.Reaction.Key), reactionContext(t, &got), &pb.Sealed{
		Epoch: got.Reaction.Epoch, Nonce: unhex(t, got.Reaction.Nonce), Ciphertext: unhex(t, got.Reaction.Ciphertext),
	})
	if err != nil || !slices.Equal(reactions.GetEmoji(), got.Reaction.Emoji) {
		t.Fatalf("open reactions: %v, %v", reactions, err)
	}
	file, err := DecryptBlob(unhex(t, got.Blob.Key), unhex(t, got.Blob.Digest), unhex(t, got.Blob.Blob))
	if err != nil || h(file) != got.Blob.Plaintext {
		t.Fatalf("decrypt blob: %x, %v", file, err)
	}
}

func TestRFC7748(t *testing.T) {
	v := inputs().RFC7748
	alice, _ := PrivateKey(unhex(t, v.AlicePrivate))
	bob, _ := PrivateKey(unhex(t, v.BobPrivate))
	if h(alice.PublicKey().Bytes()) != v.AlicePublic || h(bob.PublicKey().Bytes()) != v.BobPublic {
		t.Fatal("public keys differ from RFC 7748")
	}
	ab, err := SharedSecret(alice, unhex(t, v.BobPublic))
	if err != nil {
		t.Fatal(err)
	}
	ba, _ := SharedSecret(bob, unhex(t, v.AlicePublic))
	if h(ab) != v.Shared || h(ba) != v.Shared {
		t.Fatalf("shared secret %x / %x", ab, ba)
	}
}

func TestRFC5869(t *testing.T) {
	v := inputs().RFC5869
	okm, err := hkdf.Key(sha256.New, unhex(t, v.IKM), unhex(t, v.Salt), string(unhex(t, v.Info)), v.L)
	if err != nil || h(okm) != v.OKM {
		t.Fatalf("okm %x, %v", okm, err)
	}
	// With the salt of 32 zero bytes, as HKDF here uses, the result is the
	// same as with no salt (RFC 5869 section 2.2).
	a, _ := HKDF(unhex(t, v.IKM), unhex(t, v.Info))
	b, _ := hkdf.Key(sha256.New, unhex(t, v.IKM), nil, string(unhex(t, v.Info)), 32)
	if !bytes.Equal(a, b) {
		t.Fatal("zero salt differs from no salt")
	}
}

func TestTampering(t *testing.T) {
	a, _ := GenerateKey()
	b, _ := GenerateKey()
	w := WrapContext{ConversationID: seq(1, 16), Epoch: 2, SenderUserID: seq(2, 16), SenderDeviceID: 3, RecipientUserID: seq(4, 16), RecipientDeviceID: 5}
	k := NewEpochKey()
	wrapped, err := Wrap(a, b.PublicKey().Bytes(), w, NewNonce(), k)
	if err != nil || len(wrapped) != WrappedSize {
		t.Fatalf("wrap: %d bytes, %v", len(wrapped), err)
	}
	if got, err := Unwrap(b, a.PublicKey().Bytes(), w, wrapped); err != nil || !bytes.Equal(got, k) {
		t.Fatalf("unwrap: %v", err)
	}
	other := w
	other.Epoch = 3
	if _, err := Unwrap(b, a.PublicKey().Bytes(), other, wrapped); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("unwrap with another epoch: %v", err)
	}

	c := ContentContext{ConversationID: w.ConversationID, Epoch: 2, SenderUserID: w.SenderUserID, ClientMessageID: "m"}
	sealed, err := SealContent(k, c, &pb.Content{Body: &pb.Content_Text{Text: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	moved := c
	moved.ClientMessageID = "n"
	if _, err := OpenContent(k, moved, sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("open as another message: %v", err)
	}
	moved = c
	moved.SenderUserID = seq(9, 16)
	if _, err := OpenContent(k, moved, sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("open as another sender: %v", err)
	}
}

func TestBlob(t *testing.T) {
	file := seq(1, 1000)
	k, blob, digest, err := EncryptBlob(file)
	if err != nil || len(blob) != NonceSize+len(file)+TagSize {
		t.Fatalf("encrypt: %d bytes, %v", len(blob), err)
	}
	if got, err := DecryptBlob(k, digest, blob); err != nil || !bytes.Equal(got, file) {
		t.Fatalf("decrypt: %v", err)
	}
	tampered := slices.Clone(blob)
	tampered[NonceSize] ^= 1
	if _, err := DecryptBlob(k, digest, tampered); !errors.Is(err, ErrDigest) {
		t.Fatalf("tampered blob: %v", err)
	}
	sum := sha256.Sum256(tampered)
	if _, err := DecryptBlob(k, sum[:], tampered); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered blob with its digest: %v", err)
	}
	if _, err := DecryptBlob(NewEpochKey(), digest, blob); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("another key: %v", err)
	}
}

func TestSafetyNumberSymmetric(t *testing.T) {
	a := Fingerprint(seq(1, 16), []DeviceKey{{1, seq(1, 32)}})
	b := Fingerprint(seq(2, 16), []DeviceKey{{2, seq(2, 32)}, {1, seq(3, 32)}})
	if SafetyNumber(a, b) != SafetyNumber(b, a) || len(SafetyNumber(a, b)) != 35 {
		t.Fatal(SafetyNumber(a, b))
	}
}
