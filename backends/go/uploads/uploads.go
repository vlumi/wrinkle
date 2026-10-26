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
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// slotLifetime is how long an upload slot's PUT stays valid.
const slotLifetime = 900 * time.Second

type limit struct {
	purpose    string
	mediaTypes []string
	maxSize    uint64
}

var limits = map[pb.BlobPurpose]limit{
	pb.BlobPurpose_BLOB_PURPOSE_AVATAR:  {"avatar", []string{"image/jpeg", "image/png", "image/webp"}, 512 * 1024},
	pb.BlobPurpose_BLOB_PURPOSE_STICKER: {"sticker", []string{"image/png", "image/webp", "image/gif"}, 256 * 1024},
	// Ciphertext, served as nothing a browser would render.
	pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT: {"attachment", []string{"application/octet-stream"}, 25 * 1024 * 1024},
}

type Service struct {
	db    *sql.DB
	blobs *blobs.Store
}

func New(db *sql.DB, blobs *blobs.Store) *Service {
	return &Service{db: db, blobs: blobs}
}

// CreateUpload records an upload slot and returns the signed PUT for it.
func (s *Service) CreateUpload(ctx context.Context, userID ids.UUID, req *pb.CreateUploadRequest) (*pb.CreateUploadResponse, error) {
	l, ok := limits[req.GetPurpose()]
	if !ok {
		return nil, apierror.InvalidArgument("unknown purpose %v", req.GetPurpose())
	}
	mediaType := req.GetMediaType()
	if !slices.Contains(l.mediaTypes, mediaType) {
		return nil, apierror.InvalidArgument("%s must be one of %s", l.purpose, strings.Join(l.mediaTypes, ", "))
	}
	if req.GetSize() < 1 || req.GetSize() > l.maxSize {
		return nil, apierror.InvalidArgument("size must be 1 to %d bytes for %s", l.maxSize, l.purpose)
	}
	blobID := ids.New()
	now := vtdb.Now()
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

func notAttachmentUpload() error {
	return apierror.FailedPrecondition("not an attachment upload of yours")
}

// UseAttachments takes the user's uploaded attachment blobs into use, so
// the sweeper keeps them; the ids are distinct, and none may be in use by
// another message already. They stay pending until the fan-out delivers
// the message, so a send that fails without releasing them loses them only
// to the sweeper.
func (s *Service) UseAttachments(ctx context.Context, userID ids.UUID, blobIDs []ids.UUID) error {
	if len(blobIDs) == 0 {
		return nil
	}
	query, args := blobQuery(`
		SELECT used FROM upload WHERE user_id = ? AND purpose = 'attachment' AND blob_id IN `, userID, blobIDs)
	if _, err := attachmentUploads(ctx, s.db, query, args, len(blobIDs)); err != nil {
		return err
	}
	for _, id := range blobIDs {
		uploaded, err := s.blobs.Exists(ctx, id)
		if err != nil {
			return err
		}
		if !uploaded {
			return apierror.FailedPrecondition("not uploaded")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Locking the rows again keeps the sweeper from taking them meanwhile.
	used, err := attachmentUploads(ctx, tx, query+` FOR UPDATE`, args, len(blobIDs))
	if err != nil {
		return err
	}
	// Another message refers to it; deleting either would release it.
	if used {
		return apierror.FailedPrecondition("already attached")
	}
	query, args = blobQuery(`UPDATE upload SET used = TRUE, pending_since = ? WHERE user_id = ? AND blob_id IN `, userID, blobIDs)
	args = append([]any{vtdb.Now()}, args...)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// attachmentUploads checks that the query finds the given number of
// attachment uploads, and tells whether any of them is used.
func attachmentUploads(ctx context.Context, q vtdb.Querier, query string, args []any, want int) (bool, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	n, anyUsed := 0, false
	for rows.Next() {
		var used bool
		if err := rows.Scan(&used); err != nil {
			return false, err
		}
		n++
		anyUsed = anyUsed || used
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if n != want {
		return false, notAttachmentUpload()
	}
	return anyUsed, nil
}

// ReleaseAttachments marks the user's attachment blobs unused, so the
// sweeper deletes them.
func (s *Service) ReleaseAttachments(ctx context.Context, userID ids.UUID, blobIDs []ids.UUID) error {
	if len(blobIDs) == 0 {
		return nil
	}
	query, args := blobQuery(`
		UPDATE upload SET used = FALSE, pending_since = NULL WHERE user_id = ? AND purpose = 'attachment' AND blob_id IN `, userID, blobIDs)
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// blobQuery appends the list of blob ids to a query on one user's uploads.
func blobQuery(prefix string, userID ids.UUID, blobIDs []ids.UUID) (string, []any) {
	args := make([]any, 0, 1+len(blobIDs))
	args = append(args, userID)
	for _, id := range blobIDs {
		args = append(args, id)
	}
	return prefix + `(?` + strings.Repeat(`, ?`, len(blobIDs)-1) + `)`, args
}
