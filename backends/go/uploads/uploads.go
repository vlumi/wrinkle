// Package uploads hands out slots to upload blobs to, and deletes blobs
// nothing refers to, against the user keyspace and the blob storage.
package uploads

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/blobs"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// slotLifetime is how long an upload slot's PUT stays valid.
const slotLifetime = 900 * time.Second

const maxMediaTypeBytes = 100

type limit struct {
	purpose    string
	mediaTypes []string // nil allows any
	maxSize    uint64
}

var limits = map[pb.BlobPurpose]limit{
	pb.BlobPurpose_BLOB_PURPOSE_AVATAR:     {"avatar", []string{"image/jpeg", "image/png", "image/webp"}, 512 * 1024},
	pb.BlobPurpose_BLOB_PURPOSE_STICKER:    {"sticker", []string{"image/png", "image/webp", "image/gif"}, 256 * 1024},
	pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT: {"attachment", nil, 25 * 1024 * 1024},
}

type Service struct {
	db    *sql.DB
	blobs *blobs.Store
	now   func() time.Time
}

func New(db *sql.DB, blobs *blobs.Store) *Service {
	return &Service{db: db, blobs: blobs, now: func() time.Time { return time.Now().UTC() }}
}

// CreateUpload records an upload slot and returns the signed PUT for it.
func (s *Service) CreateUpload(ctx context.Context, userID ids.UUID, req *pb.CreateUploadRequest) (*pb.CreateUploadResponse, error) {
	l, ok := limits[req.GetPurpose()]
	if !ok {
		return nil, apierror.InvalidArgument("unknown purpose %v", req.GetPurpose())
	}
	mediaType := req.GetMediaType()
	if l.mediaTypes != nil && !slices.Contains(l.mediaTypes, mediaType) {
		return nil, apierror.InvalidArgument("%s must be one of %s", l.purpose, strings.Join(l.mediaTypes, ", "))
	}
	if !validMediaType(mediaType) {
		return nil, apierror.InvalidArgument("media_type must be type/subtype")
	}
	if req.GetSize() < 1 || req.GetSize() > l.maxSize {
		return nil, apierror.InvalidArgument("size must be 1 to %d bytes for %s", l.maxSize, l.purpose)
	}
	blobID := ids.New()
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO upload (user_id, blob_id, purpose, media_type, size, used, created_at)
		VALUES (?, ?, ?, ?, ?, FALSE, ?)`,
		userID, blobID, l.purpose, mediaType, req.GetSize(), now); err != nil {
		return nil, err
	}
	return &pb.CreateUploadResponse{
		BlobId:     blobID.Bytes(),
		UploadPath: s.blobs.PresignPut(blobID, mediaType, req.GetSize(), now, slotLifetime),
		ExpiresAt:  timestamppb.New(now.Add(slotLifetime)),
	}, nil
}

// validMediaType accepts type/subtype in printable ASCII without spaces,
// up to 100 bytes.
func validMediaType(s string) bool {
	if len(s) > maxMediaTypeBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	kind, sub, ok := strings.Cut(s, "/")
	return ok && kind != "" && sub != "" && !strings.Contains(sub, "/")
}
