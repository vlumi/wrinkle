package wrinkleclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// BlobAddress derives the blob address from a gateway URL, as frame.proto
// describes: https://host:port for wss, http://host:9200 for ws.
func BlobAddress(gatewayURL string) (string, error) {
	u, err := url.Parse(gatewayURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "wss":
		return "https://" + u.Host, nil
	case "ws":
		return "http://" + u.Hostname() + ":9200", nil
	}
	return "", fmt.Errorf("not a WebSocket URL: %s", gatewayURL)
}

// Upload PUTs the bytes to an upload path from CreateUpload, relative to the
// blob address, with the Content-Type and Content-Length the slot was made
// for; it returns the HTTP status.
func Upload(ctx context.Context, blobAddress, uploadPath, mediaType string, data []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimSuffix(blobAddress, "/")+uploadPath, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", mediaType)
	req.ContentLength = int64(len(data))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// BlobURL is where a blob is read.
func BlobURL(blobAddress string, blobID []byte) string {
	return fmt.Sprintf("%s/blobs/%x", strings.TrimSuffix(blobAddress, "/"), blobID)
}

// Download GETs a blob; it fails unless the blob address answers 200.
func Download(ctx context.Context, blobAddress string, blobID []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BlobURL(blobAddress, blobID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("blob %x: HTTP %d", blobID, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
