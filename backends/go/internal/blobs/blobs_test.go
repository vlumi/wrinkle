package blobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

// The local storage's credentials; every backend's test checks the same
// vectors, so they all sign alike.
var testConfig = Config{
	KeyID:  "GK7772696e6b6c6500000000",
	Secret: "667c7e6aafc73808d30a89b14f18816b605d7d340823d9493b32b97aa7079fc4",
	Region: "garage",
}

var (
	testTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	testBlob = ids.UUID{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
)

func TestPresignPut(t *testing.T) {
	got := New(testConfig).PresignPut(testBlob, "image/png", 1234, testTime, 900*time.Second)
	want := "/blobs/00112233445566778899aabbccddeeff?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=GK7772696e6b6c6500000000%2F20260102%2Fgarage%2Fs3%2Faws4_request&X-Amz-Date=20260102T030405Z&X-Amz-Expires=900&X-Amz-SignedHeaders=content-length%3Bcontent-type%3Bhost&X-Amz-Signature=41f91cfa6894289b36a84ba3dd2382a597515b97882580eba97a7beead3006e3"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestPresignDelete(t *testing.T) {
	got := New(testConfig).presign(http.MethodDelete, Path(testBlob), nil, testTime, 60*time.Second)
	want := "/blobs/00112233445566778899aabbccddeeff?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=GK7772696e6b6c6500000000%2F20260102%2Fgarage%2Fs3%2Faws4_request&X-Amz-Date=20260102T030405Z&X-Amz-Expires=60&X-Amz-SignedHeaders=host&X-Amz-Signature=63ca246240ae5e8ba3f157a95f71ddd3e094ee79692510754b5f3406b17b5a15"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestExistsAndDelete(t *testing.T) {
	status := map[string]int{}
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.URL.Query().Get("X-Amz-Signature") == "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			deleted = r.URL.Path
		}
		w.WriteHeader(status[r.Method])
	}))
	defer srv.Close()
	cfg := testConfig
	cfg.URL = srv.URL + "/"
	s := New(cfg)
	ctx := context.Background()

	for code, want := range map[int]bool{200: true, 404: false, 403: false} {
		status[http.MethodHead] = code
		if got, err := s.Exists(ctx, testBlob); err != nil || got != want {
			t.Fatalf("HEAD %d: %v %v", code, got, err)
		}
	}
	status[http.MethodHead] = 500
	if _, err := s.Exists(ctx, testBlob); err == nil {
		t.Fatal("HEAD 500 is not an error")
	}

	for _, code := range []int{204, 404} {
		status[http.MethodDelete] = code
		if err := s.Delete(ctx, testBlob, testTime); err != nil {
			t.Fatalf("DELETE %d: %v", code, err)
		}
	}
	if !strings.HasSuffix(deleted, "/blobs/00112233445566778899aabbccddeeff") {
		t.Fatalf("deleted %s", deleted)
	}
	status[http.MethodDelete] = 500
	if err := s.Delete(ctx, testBlob, testTime); err == nil {
		t.Fatal("DELETE 500 is not an error")
	}
}
