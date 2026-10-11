// Package blobs talks to the object storage that holds uploaded blobs,
// through the blob address: presigned PUTs for devices, and HEAD and
// DELETE requests of the services' own.
package blobs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

// signingHost is the host every request is signed for; the blob proxy sets
// it on every request, whatever address was used.
const signingHost = "blobs"

// Config is where the blob address is and the credentials to sign with.
type Config struct {
	URL    string
	KeyID  string
	Secret string
	Region string
}

type Store struct {
	url    string
	keyID  string
	secret string
	region string
	http   *http.Client
}

const requestTimeout = 5 * time.Second

func New(cfg Config) *Store {
	return &Store{
		url:    strings.TrimSuffix(cfg.URL, "/"),
		keyID:  cfg.KeyID,
		secret: cfg.Secret,
		region: cfg.Region,
		http:   &http.Client{Timeout: requestTimeout},
	}
}

// Path is where a blob is read, relative to the blob address.
func Path(blobID ids.UUID) string { return "/blobs/" + blobID.String() }

// PresignPut returns the path and query of a PUT of the blob, valid for
// the given time, that only succeeds with exactly this media type and size.
func (s *Store) PresignPut(blobID ids.UUID, mediaType string, size uint64, now time.Time, expires time.Duration) string {
	return s.presign(http.MethodPut, Path(blobID), map[string]string{
		"content-type":   mediaType,
		"content-length": strconv.FormatUint(size, 10),
	}, now, expires)
}

// Exists reports whether the blob has been uploaded.
func (s *Store) Exists(ctx context.Context, blobID ids.UUID) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.url+Path(blobID), nil)
	if err != nil {
		return false, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound, http.StatusForbidden:
		return false, nil
	}
	return false, fmt.Errorf("blob %s: HEAD answered %s", blobID, resp.Status)
}

// Delete removes the blob; one that is not there is fine.
func (s *Store) Delete(ctx context.Context, blobID ids.UUID, now time.Time) error {
	target := s.presign(http.MethodDelete, Path(blobID), nil, now, time.Minute)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.url+target, nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return fmt.Errorf("blob %s: DELETE answered %s", blobID, resp.Status)
}

// presign signs a request with AWS Signature Version 4 in the query, for
// an unsigned payload, and returns its path and query.
func (s *Store) presign(method, path string, signedHeaders map[string]string, now time.Time, expires time.Duration) string {
	now = now.UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := date + "/" + s.region + "/s3/aws4_request"

	headers := map[string]string{"host": signingHost}
	for name, value := range signedHeaders {
		headers[strings.ToLower(name)] = strings.TrimSpace(value)
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	signed := strings.Join(names, ";")

	params := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    s.keyID + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.FormatInt(int64(expires/time.Second), 10),
		"X-Amz-SignedHeaders": signed,
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = uriEncode(k) + "=" + uriEncode(params[k])
	}
	query := strings.Join(pairs, "&")

	var canonical strings.Builder
	canonical.WriteString(method + "\n" + path + "\n" + query + "\n")
	for _, name := range names {
		canonical.WriteString(name + ":" + headers[name] + "\n")
	}
	canonical.WriteString("\n" + signed + "\nUNSIGNED-PAYLOAD")

	hashed := sha256.Sum256([]byte(canonical.String()))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(hashed[:])
	key := hmacSHA256([]byte("AWS4"+s.secret), date)
	key = hmacSHA256(key, s.region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	return path + "?" + query + "&X-Amz-Signature=" + signature
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// uriEncode keeps the unreserved characters and percent-encodes every
// other byte in uppercase hex.
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
