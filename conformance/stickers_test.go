package conformance

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// newSticker makes a sticker of random bytes, returning it and the bytes.
func newSticker(t *testing.T, d *device) (*pb.Sticker, []byte) {
	t.Helper()
	image := randomBytes(t, 300)
	sticker, err := d.NewSticker(ctx(t), blobAddress(), "image/png", image)
	if err != nil {
		t.Fatalf("new sticker: %v", err)
	}
	return sticker, image
}

func stickerIDs(stickers ...*pb.Sticker) [][]byte {
	list := make([][]byte, len(stickers))
	for i, s := range stickers {
		list[i] = s.GetStickerId()
	}
	return list
}

// paletteEvent checks that the next palette event has the palette given.
func paletteEvent(t *testing.T, d *device, want ...*pb.Sticker) {
	t.Helper()
	ev := nextEvent(t, d, func(ev *pb.Event) bool { return ev.GetPalette() != nil })
	if got := ev.GetPalette().GetStickerIds(); !equalIDs(got, stickerIDs(want...)) {
		t.Fatalf("palette event %x, want %x", got, stickerIDs(want...))
	}
}

// wantPalette checks the palette GetPalette returns.
func wantPalette(t *testing.T, d *device, want ...*pb.Sticker) {
	t.Helper()
	got, err := d.GetPalette(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("palette %v, want %v", got, want)
	}
	for i := range got {
		if !proto.Equal(got[i], want[i]) {
			t.Fatalf("palette %v, want %v", got, want)
		}
	}
}

func getBlob(t *testing.T, blobID []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx(t), http.MethodGet, wrinkleclient.BlobURL(blobAddress(), blobID), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func TestCreateSticker(t *testing.T) {
	a := newUser(t, "maker")
	a2 := otherDevice(t, a)
	b := newUser(t, "other")

	_, err := a.CreateSticker(ctx(t), []byte{1, 2, 3})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = a.CreateSticker(ctx(t), randomID(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	avatar := upload(t, a, pb.BlobPurpose_BLOB_PURPOSE_AVATAR, "image/png", randomBytes(t, 100))
	_, err = a.CreateSticker(ctx(t), avatar)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	theirs := upload(t, b, pb.BlobPurpose_BLOB_PURPOSE_STICKER, "image/gif", randomBytes(t, 100))
	_, err = a.CreateSticker(ctx(t), theirs)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	image := randomBytes(t, 500)
	slot, err := a.CreateUpload(ctx(t), pb.BlobPurpose_BLOB_PURPOSE_STICKER, "image/webp", uint64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.CreateSticker(ctx(t), slot.GetBlobId())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)

	status, err := wrinkleclient.Upload(ctx(t), blobAddress(), slot.GetUploadPath(), "image/webp", image)
	if err != nil || status != http.StatusOK {
		t.Fatalf("PUT: %d %v", status, err)
	}
	before := time.Now().Add(-time.Minute)
	sticker, err := a.CreateSticker(ctx(t), slot.GetBlobId())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sticker.GetStickerId(), slot.GetBlobId()) || !bytes.Equal(sticker.GetCreatorUserId(), a.user.GetUserId()) ||
		sticker.GetMediaType() != "image/webp" || sticker.GetCreatedAt().AsTime().Before(before) {
		t.Fatalf("sticker %v", sticker)
	}
	// Saved to the creator's palette, on every device.
	paletteEvent(t, a, sticker)
	paletteEvent(t, a2, sticker)
	wantPalette(t, a, sticker)

	// Making it again returns it, and changes nothing.
	again, err := a.CreateSticker(ctx(t), slot.GetBlobId())
	if err != nil || !proto.Equal(again, sticker) {
		t.Fatalf("again: %v %v", again, err)
	}
	// Another user cannot make it theirs.
	_, err = b.CreateSticker(ctx(t), slot.GetBlobId())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	if got, err := b.GetStickers(ctx(t), stickerIDs(sticker)); err != nil || len(got) != 1 || !proto.Equal(got[0], sticker) {
		t.Fatalf("get: %v %v", got, err)
	}

	// The image is public.
	if status, data := getBlob(t, sticker.GetStickerId()); status != http.StatusOK || !bytes.Equal(data, image) {
		t.Fatalf("GET: %d, %d bytes", status, len(data))
	}

	// Removed from the palette, it stays a sticker; making it again
	// does not save it again.
	if err := a.RemoveSticker(ctx(t), sticker.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	paletteEvent(t, a)
	if again, err := a.CreateSticker(ctx(t), slot.GetBlobId()); err != nil || !proto.Equal(again, sticker) {
		t.Fatalf("again after removing: %v %v", again, err)
	}
	other, _ := newSticker(t, a)
	paletteEvent(t, a, other)
}

func TestGetStickers(t *testing.T) {
	a := newUser(t, "maker")
	b := newUser(t, "viewer")
	s1, _ := newSticker(t, a)
	s2, _ := newSticker(t, a)

	got, err := b.GetStickers(ctx(t), [][]byte{s2.GetStickerId(), randomID(t), s1.GetStickerId()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !proto.Equal(got[0], s2) || !proto.Equal(got[1], s1) {
		t.Fatalf("got %v", got)
	}
	unknown := make([][]byte, 100)
	for i := range unknown {
		unknown[i] = randomID(t)
	}
	if got, err := b.GetStickers(ctx(t), unknown); err != nil || len(got) != 0 {
		t.Fatalf("100 unknown: %v %v", got, err)
	}
	_, err = b.GetStickers(ctx(t), append(unknown, s1.GetStickerId()))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = b.GetStickers(ctx(t), nil)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = b.GetStickers(ctx(t), [][]byte{s1.GetStickerId(), {1, 2, 3}})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
}

func TestPalette(t *testing.T) {
	a := newUser(t, "maker")
	s1, _ := newSticker(t, a)
	s2, _ := newSticker(t, a)
	s3, _ := newSticker(t, a)
	paletteEvent(t, a, s1)
	paletteEvent(t, a, s1, s2)
	paletteEvent(t, a, s1, s2, s3)

	// Anyone may save anyone's sticker. Each check of a palette event
	// takes the next one, so a request that should change nothing and
	// wrote an event fails the following check.
	b := newUser(t, "saver")
	b2 := otherDevice(t, b)
	wantPalette(t, b)
	if err := b.SaveSticker(ctx(t), s2.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	paletteEvent(t, b, s2)
	paletteEvent(t, b2, s2)
	err := b.SaveSticker(ctx(t), randomID(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	err = b.SaveSticker(ctx(t), []byte{1})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	if err := b.SaveSticker(ctx(t), s2.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*pb.Sticker{s1, s3} {
		if err := b.SaveSticker(ctx(t), s.GetStickerId()); err != nil {
			t.Fatal(err)
		}
	}
	paletteEvent(t, b, s2, s1)
	paletteEvent(t, b, s2, s1, s3)
	wantPalette(t, b, s2, s1, s3)

	move := func(s, before *pb.Sticker) error {
		return b.MoveSticker(ctx(t), s.GetStickerId(), before.GetStickerId())
	}
	if err := move(s3, s2); err != nil {
		t.Fatal(err)
	}
	paletteEvent(t, b, s3, s2, s1)
	wantPalette(t, b, s3, s2, s1)
	if err := move(s3, nil); err != nil {
		t.Fatal(err)
	}
	paletteEvent(t, b, s2, s1, s3)
	// Moves that leave the order as it is.
	for _, m := range [][2]*pb.Sticker{{s3, nil}, {s1, s1}, {s2, s1}} {
		if err := move(m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := move(s1, s2); err != nil {
		t.Fatal(err)
	}
	paletteEvent(t, b, s1, s2, s3)
	wantPalette(t, b, s1, s2, s3)

	other, _ := newSticker(t, a)
	paletteEvent(t, a, s1, s2, s3, other)
	err = move(other, nil)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	err = move(s1, other)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	err = b.MoveSticker(ctx(t), randomID(t), nil)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	err = b.MoveSticker(ctx(t), s1.GetStickerId(), []byte{1})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	if err := b.RemoveSticker(ctx(t), s2.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	paletteEvent(t, b, s1, s3)
	// Removing what is not saved changes nothing.
	for _, id := range [][]byte{s2.GetStickerId(), other.GetStickerId(), randomID(t)} {
		if err := b.RemoveSticker(ctx(t), id); err != nil {
			t.Fatal(err)
		}
	}
	err = b.RemoveSticker(ctx(t), nil)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	// A removed sticker saved again goes to the end.
	if err := b.SaveSticker(ctx(t), s2.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	paletteEvent(t, b, s1, s3, s2)
	wantPalette(t, b, s1, s3, s2)
	paletteEvent(t, b2, s2, s1)
	// The creator's palette is their own.
	wantPalette(t, a, s1, s2, s3, other)
}

// TestPaletteFull fills a palette to its 200 stickers, making them on
// several devices at once to stay within each connection's request rate.
func TestPaletteFull(t *testing.T) {
	const full, devices = 200, 8
	a := newUser(t, "collector")
	ds := []*device{a}
	for len(ds) < devices {
		ds = append(ds, otherDeviceWithoutKey(t, a))
	}
	var wg sync.WaitGroup
	errs := make(chan error, devices)
	for i, d := range ds {
		wg.Go(func() {
			for j := i; j < full; j += devices {
				if _, err := d.NewSticker(ctx(t), blobAddress(), "image/png", randomBytes(t, 50)); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	palette, err := a.GetPalette(ctx(t))
	if err != nil || len(palette) != full {
		t.Fatalf("palette of %d: %v", len(palette), err)
	}
	// A full palette does not keep a sticker from being made, only saved.
	extra, _ := newSticker(t, ds[1])
	if palette, err := a.GetPalette(ctx(t)); err != nil || len(palette) != full {
		t.Fatalf("palette of %d after making one more: %v", len(palette), err)
	}
	err = a.SaveSticker(ctx(t), extra.GetStickerId())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	// One already saved is still fine.
	if err := a.SaveSticker(ctx(t), palette[0].GetStickerId()); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveSticker(ctx(t), palette[0].GetStickerId()); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveSticker(ctx(t), extra.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	palette, err = a.GetPalette(ctx(t))
	if err != nil || len(palette) != full || !proto.Equal(palette[full-1], extra) {
		t.Fatalf("palette of %d after making room: %v", len(palette), err)
	}
}

func TestStickerMessage(t *testing.T) {
	a, b, conv := pair(t)
	sticker, _ := newSticker(t, a)
	paletteEvent(t, a, sticker)
	if _, err := a.SendSticker(ctx(t), conv, "m1", sticker.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	m := messageEvent(t, b, conv, 1)
	content, err := b.OpenMessage(ctx(t), m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content.GetSticker().GetStickerId(), sticker.GetStickerId()) {
		t.Fatalf("content %v", content)
	}
	got, err := b.GetStickers(ctx(t), [][]byte{content.GetSticker().GetStickerId()})
	if err != nil || len(got) != 1 || !proto.Equal(got[0], sticker) {
		t.Fatalf("get: %v %v", got, err)
	}
	if err := b.SaveSticker(ctx(t), sticker.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	paletteEvent(t, b, sticker)
}

func TestDeleteAccountKeepsStickers(t *testing.T) {
	a, b, conv := pair(t)
	sticker, image := newSticker(t, a)
	theirs, _ := newSticker(t, b)
	if err := a.SaveSticker(ctx(t), theirs.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveSticker(ctx(t), sticker.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendSticker(ctx(t), conv, "m1", sticker.GetStickerId()); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, 1)
	if err := a.DeleteAccount(ctx(t), password); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, b, func(ev *pb.Event) bool {
		u := ev.GetProfile().GetUser()
		return u.GetDeleted() && bytes.Equal(u.GetUserId(), a.user.GetUserId())
	})

	// The deleted user's stickers stay, for everyone who has them.
	got, err := b.GetStickers(ctx(t), stickerIDs(sticker, theirs))
	if err != nil || len(got) != 2 || !proto.Equal(got[0], sticker) || !proto.Equal(got[1], theirs) {
		t.Fatalf("get: %v %v", got, err)
	}
	wantPalette(t, b, theirs, sticker)
	if status, data := getBlob(t, sticker.GetStickerId()); status != http.StatusOK || !bytes.Equal(data, image) {
		t.Fatalf("GET: %d, %d bytes", status, len(data))
	}
}
