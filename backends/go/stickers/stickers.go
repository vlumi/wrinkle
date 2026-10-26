// Package stickers is the sticker service: stickers made from uploaded
// blobs, public and permanent, and each user's palette of saved ones,
// against the user keyspace.
package stickers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/blobs"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	maxPalette = 200
	maxGetIDs  = 100
)

type Service struct {
	db    *sql.DB
	blobs *blobs.Store
}

func New(db *sql.DB, blobs *blobs.Store) *Service {
	return &Service{db: db, blobs: blobs}
}

func parseID(b []byte, field string) (ids.UUID, error) {
	id, ok := ids.Parse(b)
	if !ok {
		return id, apierror.InvalidArgument("%s must be 16 bytes", field)
	}
	return id, nil
}

// Create makes the user's uploaded sticker blob a sticker: the upload is
// marked used first, so the sweeper keeps the blob, then the sticker row
// goes in on its own shard and the sticker is saved to the creator's
// palette unless that is full. Making it again returns the sticker.
func (s *Service) Create(ctx context.Context, userID ids.UUID, blob []byte) (*pb.Sticker, error) {
	blobID, err := parseID(blob, "blob_id")
	if err != nil {
		return nil, err
	}
	var mediaType string
	err = s.db.QueryRowContext(ctx, `
		SELECT media_type FROM upload WHERE user_id = ? AND blob_id = ? AND purpose = 'sticker'`,
		userID, blobID).Scan(&mediaType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notStickerUpload()
	}
	if err != nil {
		return nil, err
	}
	uploaded, err := s.blobs.Exists(ctx, blobID)
	if err != nil {
		return nil, err
	}
	if !uploaded {
		return nil, apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_NOT_UPLOADED, "not uploaded")
	}
	if existing, err := s.load(ctx, []ids.UUID{blobID}); err != nil || len(existing) > 0 {
		if err != nil {
			return nil, err
		}
		return existing[0], nil
	}
	if err := s.markUsed(ctx, userID, blobID); err != nil {
		return nil, err
	}
	sticker := &pb.Sticker{
		StickerId:     blobID.Bytes(),
		CreatorUserId: userID.Bytes(),
		MediaType:     mediaType,
	}
	now := vtdb.Now()
	sticker.CreatedAt = timestamppb.New(now)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sticker (sticker_id, creator_user_id, media_type, created_at) VALUES (?, ?, ?, ?)`,
		blobID, userID, mediaType, now); err != nil {
		if !vtdb.IsDuplicate(err) {
			return nil, err
		}
		existing, err := s.load(ctx, []ids.UUID{blobID})
		if err != nil {
			return nil, err
		}
		if len(existing) == 0 {
			return nil, errors.New("sticker missing after a duplicate key")
		}
		return existing[0], nil
	}
	if err := s.Save(ctx, userID, blobID.Bytes()); err != nil && apierror.Code(err) != pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION {
		return nil, err
	}
	return sticker, nil
}

func notStickerUpload() error {
	return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_NOT_YOUR_UPLOAD, "not a sticker upload of yours")
}

// markUsed takes the upload into use, unless the sweeper took it first.
func (s *Service) markUsed(ctx context.Context, userID, blobID ids.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var one int
	err = tx.QueryRowContext(ctx, `
		SELECT 1 FROM upload WHERE user_id = ? AND blob_id = ? AND purpose = 'sticker' FOR UPDATE`,
		userID, blobID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return notStickerUpload()
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE upload SET used = TRUE WHERE user_id = ? AND blob_id = ?`, userID, blobID); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns the known stickers among 1 to 100 ids, in the order asked,
// each once.
func (s *Service) Get(ctx context.Context, stickerIDs [][]byte) ([]*pb.Sticker, error) {
	if len(stickerIDs) < 1 || len(stickerIDs) > maxGetIDs {
		return nil, apierror.InvalidArgument("sticker_ids must have 1 to %d ids", maxGetIDs)
	}
	list := make([]ids.UUID, 0, len(stickerIDs))
	seen := make(map[ids.UUID]bool, len(stickerIDs))
	for _, b := range stickerIDs {
		id, err := parseID(b, "sticker_ids")
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			list = append(list, id)
		}
	}
	return s.load(ctx, list)
}

// load reads stickers by id, in the order given, leaving out unknown ones.
func (s *Service) load(ctx context.Context, stickerIDs []ids.UUID) ([]*pb.Sticker, error) {
	if len(stickerIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(stickerIDs))
	for i, id := range stickerIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sticker_id, creator_user_id, media_type, created_at FROM sticker
		WHERE sticker_id IN (?`+strings.Repeat(", ?", len(args)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[ids.UUID]*pb.Sticker, len(stickerIDs))
	for rows.Next() {
		var id, creator ids.UUID
		var mediaType string
		var created time.Time
		if err := rows.Scan(&id, &creator, &mediaType, &created); err != nil {
			return nil, err
		}
		found[id] = &pb.Sticker{
			StickerId:     id.Bytes(),
			CreatorUserId: creator.Bytes(),
			MediaType:     mediaType,
			CreatedAt:     timestamppb.New(created),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	list := make([]*pb.Sticker, 0, len(found))
	for _, id := range stickerIDs {
		if st, ok := found[id]; ok {
			list = append(list, st)
		}
	}
	return list, nil
}
