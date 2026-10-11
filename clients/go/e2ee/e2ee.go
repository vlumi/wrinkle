// Package e2ee is the end-to-end cryptography of content.proto: device key
// pairs, wrapping conversation epoch keys for devices, sealing content and
// reactions, encrypting attachment blobs, fingerprints and safety numbers. Functions that need a nonce take it, so
// the test vectors are reproducible; callers pass random ones.
package e2ee

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	KeySize     = 32
	NonceSize   = 12
	TagSize     = 16
	WrappedSize = NonceSize + KeySize + TagSize

	wrapLabel        = "wrinkle wrap v1"
	contentLabel     = "wrinkle content v1"
	reactionLabel    = "wrinkle reaction v1"
	blobLabel        = "wrinkle blob v1"
	fingerprintLabel = "wrinkle fingerprint v1"
)

var (
	ErrDecrypt = errors.New("e2ee: cannot decrypt")
	ErrDigest  = errors.New("e2ee: blob digest differs")
)

// GenerateKey makes a device key pair.
func GenerateKey() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// PrivateKey restores a device key pair from its 32-byte scalar.
func PrivateKey(scalar []byte) (*ecdh.PrivateKey, error) {
	return ecdh.X25519().NewPrivateKey(scalar)
}

// NewEpochKey makes a random conversation epoch key.
func NewEpochKey() []byte {
	return random(KeySize)
}

// NewNonce makes a random AES-GCM nonce.
func NewNonce() []byte {
	return random(NonceSize)
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// SharedSecret is X25519 of a private key and another device's public key.
func SharedSecret(own *ecdh.PrivateKey, otherPublic []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(otherPublic)
	if err != nil {
		return nil, err
	}
	return own.ECDH(pub)
}

// HKDF is HKDF-SHA256 with a salt of 32 zero bytes and 32 bytes out.
func HKDF(ikm, info []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, ikm, make([]byte, sha256.Size), string(info), KeySize)
}

// WrapContext names the epoch key a wrap carries and its two devices.
type WrapContext struct {
	ConversationID    []byte
	Epoch             uint32
	SenderUserID      []byte
	SenderDeviceID    uint64
	RecipientUserID   []byte
	RecipientDeviceID uint64
}

// Info is the HKDF info of a wrap.
func (w WrapContext) Info() []byte {
	b := []byte(wrapLabel)
	b = append(b, w.ConversationID...)
	b = binary.BigEndian.AppendUint32(b, w.Epoch)
	b = append(b, w.SenderUserID...)
	b = binary.BigEndian.AppendUint64(b, w.SenderDeviceID)
	b = append(b, w.RecipientUserID...)
	b = binary.BigEndian.AppendUint64(b, w.RecipientDeviceID)
	return b
}

// Wrap encrypts epoch key k for the recipient device, from the sender.
func Wrap(sender *ecdh.PrivateKey, recipientPublic []byte, w WrapContext, nonce, k []byte) ([]byte, error) {
	if len(k) != KeySize || len(nonce) != NonceSize {
		return nil, errors.New("e2ee: wrong key or nonce size")
	}
	gcm, err := wrapCipher(sender, recipientPublic, w)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(slices.Clone(nonce), nonce, k, nil), nil
}

// Unwrap decrypts an epoch key wrapped for the recipient by the sender.
func Unwrap(recipient *ecdh.PrivateKey, senderPublic []byte, w WrapContext, wrapped []byte) ([]byte, error) {
	if len(wrapped) != WrappedSize {
		return nil, ErrDecrypt
	}
	gcm, err := wrapCipher(recipient, senderPublic, w)
	if err != nil {
		return nil, err
	}
	k, err := gcm.Open(nil, wrapped[:NonceSize], wrapped[NonceSize:], nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return k, nil
}

func wrapCipher(own *ecdh.PrivateKey, otherPublic []byte, w WrapContext) (cipher.AEAD, error) {
	secret, err := SharedSecret(own, otherPublic)
	if err != nil {
		return nil, err
	}
	wk, err := HKDF(secret, w.Info())
	if err != nil {
		return nil, err
	}
	return newGCM(wk)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, errors.New("e2ee: wrong key size")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ContentContext identifies a message as the server delivers it.
type ContentContext struct {
	ConversationID  []byte
	Epoch           uint32
	SenderUserID    []byte
	ClientMessageID string
}

// AAD is the associated data of sealed content.
func (c ContentContext) AAD() []byte {
	b := []byte(contentLabel)
	b = append(b, c.ConversationID...)
	b = binary.BigEndian.AppendUint32(b, c.Epoch)
	b = append(b, c.SenderUserID...)
	return append(b, c.ClientMessageID...)
}

// ReactionContext identifies a user's reactions to a message.
type ReactionContext struct {
	ConversationID []byte
	Epoch          uint32
	UserID         []byte
	Seq            uint64
}

// AAD is the associated data of sealed reactions.
func (c ReactionContext) AAD() []byte {
	b := []byte(reactionLabel)
	b = append(b, c.ConversationID...)
	b = binary.BigEndian.AppendUint32(b, c.Epoch)
	b = append(b, c.UserID...)
	return binary.BigEndian.AppendUint64(b, c.Seq)
}

// Context is what a sealed plaintext is bound to.
type Context interface {
	AAD() []byte
}

// Seal encrypts plaintext under epoch key k; the result has the tag
// appended.
func Seal(k, nonce, plaintext []byte, c Context) ([]byte, error) {
	if len(nonce) != NonceSize {
		return nil, errors.New("e2ee: wrong nonce size")
	}
	gcm, err := newGCM(k)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, c.AAD()), nil
}

// Open decrypts what Seal made.
func Open(k, nonce, ciphertext []byte, c Context) ([]byte, error) {
	if len(nonce) != NonceSize {
		return nil, ErrDecrypt
	}
	gcm, err := newGCM(k)
	if err != nil {
		return nil, err
	}
	p, err := gcm.Open(nil, nonce, ciphertext, c.AAD())
	if err != nil {
		return nil, ErrDecrypt
	}
	return p, nil
}

// MarshalContent serializes content as devices seal it.
func MarshalContent(content *pb.Content) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(content)
}

// SealContent seals content under epoch key k of c.Epoch with a random
// nonce.
func SealContent(k []byte, c ContentContext, content *pb.Content) (*pb.Sealed, error) {
	plaintext, err := MarshalContent(content)
	if err != nil {
		return nil, err
	}
	nonce := NewNonce()
	ct, err := Seal(k, nonce, plaintext, c)
	if err != nil {
		return nil, err
	}
	return &pb.Sealed{Epoch: c.Epoch, Nonce: nonce, Ciphertext: ct}, nil
}

// OpenContent opens sealed content of a message; c.Epoch must be the
// sealed epoch, whose key k is.
func OpenContent(k []byte, c ContentContext, sealed *pb.Sealed) (*pb.Content, error) {
	if sealed.GetEpoch() != c.Epoch {
		return nil, fmt.Errorf("e2ee: sealed with epoch %d, not %d", sealed.GetEpoch(), c.Epoch)
	}
	plaintext, err := Open(k, sealed.GetNonce(), sealed.GetCiphertext(), c)
	if err != nil {
		return nil, err
	}
	content := &pb.Content{}
	if err := proto.Unmarshal(plaintext, content); err != nil {
		return nil, ErrDecrypt
	}
	return content, nil
}

// MarshalReactions serializes reactions as devices seal them.
func MarshalReactions(r *pb.Reactions) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(r)
}

// SealReactions seals a user's reactions under epoch key k of c.Epoch with
// a random nonce.
func SealReactions(k []byte, c ReactionContext, r *pb.Reactions) (*pb.Sealed, error) {
	plaintext, err := MarshalReactions(r)
	if err != nil {
		return nil, err
	}
	nonce := NewNonce()
	ct, err := Seal(k, nonce, plaintext, c)
	if err != nil {
		return nil, err
	}
	return &pb.Sealed{Epoch: c.Epoch, Nonce: nonce, Ciphertext: ct}, nil
}

// OpenReactions opens a user's sealed reactions; c.Epoch must be the
// sealed epoch, whose key k is.
func OpenReactions(k []byte, c ReactionContext, sealed *pb.Sealed) (*pb.Reactions, error) {
	if sealed.GetEpoch() != c.Epoch {
		return nil, fmt.Errorf("e2ee: sealed with epoch %d, not %d", sealed.GetEpoch(), c.Epoch)
	}
	plaintext, err := Open(k, sealed.GetNonce(), sealed.GetCiphertext(), c)
	if err != nil {
		return nil, err
	}
	r := &pb.Reactions{}
	if err := proto.Unmarshal(plaintext, r); err != nil {
		return nil, ErrDecrypt
	}
	return r, nil
}

// SealBlob encrypts a file under its key k: nonce || AES-GCM(k, nonce,
// file, "wrinkle blob v1"), as uploaded.
func SealBlob(k, nonce, file []byte) ([]byte, error) {
	if len(nonce) != NonceSize {
		return nil, errors.New("e2ee: wrong nonce size")
	}
	gcm, err := newGCM(k)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(slices.Clone(nonce), nonce, file, []byte(blobLabel)), nil
}

// EncryptBlob encrypts a file with a new random key, returning the key,
// the blob to upload and its SHA-256.
func EncryptBlob(file []byte) (k, blob, digest []byte, err error) {
	k = NewEpochKey()
	if blob, err = SealBlob(k, NewNonce(), file); err != nil {
		return nil, nil, nil, err
	}
	sum := sha256.Sum256(blob)
	return k, blob, sum[:], nil
}

// DecryptBlob checks a downloaded blob against its digest and decrypts it
// with the file's key.
func DecryptBlob(k, digest, blob []byte) ([]byte, error) {
	sum := sha256.Sum256(blob)
	if !bytes.Equal(sum[:], digest) {
		return nil, ErrDigest
	}
	if len(blob) < NonceSize+TagSize {
		return nil, ErrDecrypt
	}
	gcm, err := newGCM(k)
	if err != nil {
		return nil, err
	}
	file, err := gcm.Open(nil, blob[:NonceSize], blob[NonceSize:], []byte(blobLabel))
	if err != nil {
		return nil, ErrDecrypt
	}
	return file, nil
}

// DeviceKey is a device's public key in a user's fingerprint.
type DeviceKey struct {
	DeviceID  uint64
	PublicKey []byte
}

// Fingerprint is a user's fingerprint over their devices' keys, in any
// order.
func Fingerprint(userID []byte, devices []DeviceKey) []byte {
	sorted := slices.Clone(devices)
	slices.SortFunc(sorted, func(a, b DeviceKey) int {
		switch {
		case a.DeviceID < b.DeviceID:
			return -1
		case a.DeviceID > b.DeviceID:
			return 1
		}
		return 0
	})
	h := sha256.New()
	h.Write([]byte(fingerprintLabel))
	h.Write(userID)
	for _, d := range sorted {
		h.Write(binary.BigEndian.AppendUint64(nil, d.DeviceID))
		h.Write(d.PublicKey)
	}
	return h.Sum(nil)
}

// SafetyNumber combines two users' fingerprints, in either order, into
// six groups of five digits.
func SafetyNumber(a, b []byte) string {
	if bytes.Compare(a, b) > 0 {
		a, b = b, a
	}
	h := sha256.Sum256(append(slices.Clone(a), b...))
	var out []byte
	for i := range 6 {
		var n uint64
		for _, c := range h[i*5 : i*5+5] {
			n = n<<8 | uint64(c)
		}
		if i > 0 {
			out = append(out, ' ')
		}
		out = fmt.Appendf(out, "%05d", n%100000)
	}
	return string(out)
}
