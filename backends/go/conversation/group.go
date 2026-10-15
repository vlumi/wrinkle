package conversation

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/label"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	maxTopicBytes         = 200
	maxClientGroupIDBytes = 64
	// MaxMembers is the most members a group has.
	MaxMembers = 256
)

// CheckTopic validates a group topic.
func CheckTopic(topic string) error {
	if !label.Valid(topic, maxTopicBytes) {
		return apierror.InvalidArgument("topic must be 1 to %d bytes of UTF-8, without control characters or white space at either end", maxTopicBytes)
	}
	return nil
}

// CheckClientGroupID validates the id a device chose for a new group.
func CheckClientGroupID(id string) error {
	if id == "" || len(id) > maxClientGroupIDBytes || !utf8.ValidString(id) {
		return apierror.InvalidArgument("client_group_id must be 1 to %d bytes of UTF-8", maxClientGroupIDBytes)
	}
	return nil
}

// CreateGroup creates a group of the creator, its admin, and the invitees,
// with a GroupCreated notice as its first message. The claim on the
// creator's client id makes a retry return the group created the first
// time, and completes one whose creation stopped half-way.
func (s *Service) CreateGroup(ctx context.Context, creatorID ids.UUID, topic, clientGroupID string, invitees []ids.UUID) (*pb.Conversation, error) {
	if err := CheckTopic(topic); err != nil {
		return nil, err
	}
	if err := CheckClientGroupID(clientGroupID); err != nil {
		return nil, err
	}
	if len(invitees)+1 > MaxMembers {
		return nil, apierror.FailedPrecondition("a group has at most %d members", MaxMembers)
	}
	conversationID, err := s.claimGroup(ctx, creatorID, clientGroupID)
	if err != nil {
		return nil, err
	}
	profiles, err := s.snapshots(ctx, invitees)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = load(ctx, tx, selectStateForUpdate, conversationID)
	if err == nil {
		// Created by an earlier attempt.
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return s.Describe(ctx, conversationID, creatorID)
	}
	if apierror.Code(err) != pb.ErrorCode_ERROR_CODE_NOT_FOUND {
		return nil, err
	}
	now := vtdb.Now()
	st := state{kind: kindGroup, topic: topic, members: append([]ids.UUID{creatorID}, invitees...), lastSeq: 1}
	st.members = slices.SortedFunc(slices.Values(st.members), compareIDs)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO conversation (conversation_id, kind, topic, members, last_seq, created_at)
		VALUES (?, ?, ?, ?, 0, ?)`,
		conversationID, kindGroup, topic, encodeMembers(st.members), now); err != nil {
		return nil, err
	}
	values := make([]string, 0, len(st.members))
	args := make([]any, 0, 4*len(st.members))
	for _, id := range append([]ids.UUID{creatorID}, invitees...) {
		values = append(values, "(?, ?, ?, 1, NULL, ?)")
		args = append(args, conversationID, id, id == creatorID, now)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_member (conversation_id, user_id, admin, joined_seq, left_seq, joined_at)
		VALUES `+strings.Join(values, ", "), args...); err != nil {
		return nil, err
	}
	notice := &pb.Notice{Kind: &pb.Notice_GroupCreated{GroupCreated: &pb.GroupCreated{Topic: topic, Members: profiles}}}
	if err := storeNotice(ctx, tx, conversationID, st, creatorID, notice, st.members); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Describe(ctx, conversationID, creatorID)
}

// GroupExists reports whether the creator's client group id already names
// a group, so that a retry returns it as created.
func (s *Service) GroupExists(ctx context.Context, creatorID ids.UUID, clientGroupID string) (bool, error) {
	var id ids.UUID
	err := s.db.QueryRowContext(ctx, `
		SELECT conversation_id FROM group_claim WHERE creator_user_id = ? AND client_group_id = ?`,
		creatorID, clientGroupID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = load(ctx, s.db, selectState, id)
	if apierror.Code(err) == pb.ErrorCode_ERROR_CODE_NOT_FOUND {
		return false, nil
	}
	return err == nil, err
}

// claimGroup returns the group id claimed for the creator's client id,
// claiming a new one when there is none.
func (s *Service) claimGroup(ctx context.Context, creatorID ids.UUID, clientGroupID string) (ids.UUID, error) {
	find := func() (ids.UUID, bool, error) {
		var id ids.UUID
		err := s.db.QueryRowContext(ctx, `
			SELECT conversation_id FROM group_claim WHERE creator_user_id = ? AND client_group_id = ?`,
			creatorID, clientGroupID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return id, false, nil
		}
		return id, err == nil, err
	}
	id, found, err := find()
	if err != nil || found {
		return id, err
	}
	id = ids.New()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO group_claim (creator_user_id, client_group_id, conversation_id, created_at) VALUES (?, ?, ?, ?)`,
		creatorID, clientGroupID, id, vtdb.Now())
	if err == nil {
		return id, nil
	}
	if !vtdb.IsDuplicate(err) {
		return id, err
	}
	if id, found, err = find(); err == nil && !found {
		return id, errors.New("group claim missing after a duplicate key")
	}
	return id, err
}

// AddMembers adds the users who are not members yet, any member may. They
// read the group from the notice that adds them. check vets the users to
// add, before the group is locked and again for any who left meanwhile.
func (s *Service) AddMembers(ctx context.Context, userID, conversationID ids.UUID, users []ids.UUID, check func(context.Context, []ids.UUID) error) error {
	st, err := group(ctx, s.db, selectState, conversationID, userID)
	if err != nil {
		return err
	}
	checked := notMembers(st, users)
	if err := check(ctx, checked); err != nil {
		return err
	}
	profiles, err := s.profileMap(ctx, users)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if st, err = group(ctx, tx, selectStateForUpdate, conversationID, userID); err != nil {
		return err
	}
	added := notMembers(st, users)
	if len(added) == 0 {
		return tx.Commit()
	}
	var unchecked []ids.UUID
	for _, id := range added {
		if !slices.Contains(checked, id) {
			unchecked = append(unchecked, id)
		}
	}
	if len(unchecked) > 0 {
		if err := check(ctx, unchecked); err != nil {
			return err
		}
	}
	if len(st.members)+len(added) > MaxMembers {
		return apierror.FailedPrecondition("a group has at most %d members", MaxMembers)
	}
	st.lastSeq++
	now := vtdb.Now()
	snapshots := make([]*pb.User, 0, len(added))
	for _, id := range added {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO conversation_member (conversation_id, user_id, admin, joined_seq, left_seq, joined_at)
			VALUES (?, ?, FALSE, ?, NULL, ?)
			ON DUPLICATE KEY UPDATE admin = FALSE, joined_seq = VALUES(joined_seq), left_seq = NULL, joined_at = VALUES(joined_at)`,
			conversationID, id, st.lastSeq, now); err != nil {
			return err
		}
		snapshots = append(snapshots, profiles[id])
	}
	st.members = slices.SortedFunc(slices.Values(append(st.members, added...)), compareIDs)
	st.membershipChanged()
	notice := &pb.Notice{Kind: &pb.Notice_MembersAdded{MembersAdded: &pb.MembersAdded{Users: snapshots}}}
	if err := storeNotice(ctx, tx, conversationID, st, userID, notice, st.members); err != nil {
		return err
	}
	return tx.Commit()
}

// notMembers is the users not in the group, each once.
func notMembers(st state, users []ids.UUID) []ids.UUID {
	var list []ids.UUID
	for _, id := range users {
		if !st.has(id) && !slices.Contains(list, id) {
			list = append(list, id)
		}
	}
	return list
}

// RemoveMember removes another member; only admins may.
func (s *Service) RemoveMember(ctx context.Context, userID, conversationID, targetID ids.UUID) error {
	profiles, err := s.profileMap(ctx, []ids.UUID{targetID})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := group(ctx, tx, selectStateForUpdate, conversationID, userID)
	if err != nil {
		return err
	}
	if err := requireAdmin(ctx, tx, conversationID, userID); err != nil {
		return err
	}
	if targetID == userID {
		return apierror.InvalidArgument("removing oneself is leaving the group")
	}
	if !st.has(targetID) {
		return apierror.NotFound("user %s is not in group %s", targetID, conversationID)
	}
	before := st.members
	st.lastSeq++
	if err := s.leftAt(ctx, tx, conversationID, targetID, st.lastSeq); err != nil {
		return err
	}
	st.members = without(st.members, targetID)
	st.membershipChanged()
	notice := &pb.Notice{Kind: &pb.Notice_MemberRemoved{MemberRemoved: &pb.MemberRemoved{User: profiles[targetID]}}}
	if err := storeNotice(ctx, tx, conversationID, st, userID, notice, before); err != nil {
		return err
	}
	return tx.Commit()
}

// Leave takes the user out of a group. When the last admin leaves, the
// member who joined first becomes admin.
func (s *Service) Leave(ctx context.Context, userID, conversationID ids.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := group(ctx, tx, selectStateForUpdate, conversationID, userID)
	if err != nil {
		return err
	}
	admin, err := isAdmin(ctx, tx, conversationID, userID)
	if err != nil {
		return err
	}
	before := st.members
	st.lastSeq++
	if err := s.leftAt(ctx, tx, conversationID, userID, st.lastSeq); err != nil {
		return err
	}
	st.members = without(st.members, userID)
	st.membershipChanged()
	left := &pb.MemberLeft{}
	if admin && len(st.members) > 0 {
		if left.Promoted, err = s.promote(ctx, tx, conversationID); err != nil {
			return err
		}
	}
	notice := &pb.Notice{Kind: &pb.Notice_MemberLeft{MemberLeft: left}}
	if err := storeNotice(ctx, tx, conversationID, st, userID, notice, before); err != nil {
		return err
	}
	return tx.Commit()
}

// promote makes the member who joined first an admin when no current
// member is one, and returns their snapshot, or nil.
func (s *Service) promote(ctx context.Context, tx *sql.Tx, conversationID ids.UUID) (*pb.User, error) {
	var admins int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM conversation_member WHERE conversation_id = ? AND left_seq IS NULL AND admin`,
		conversationID).Scan(&admins); err != nil {
		return nil, err
	}
	if admins > 0 {
		return nil, nil
	}
	var first ids.UUID
	if err := tx.QueryRowContext(ctx, `
		SELECT user_id FROM conversation_member WHERE conversation_id = ? AND left_seq IS NULL
		ORDER BY joined_seq, joined_at, user_id LIMIT 1`,
		conversationID).Scan(&first); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_member SET admin = TRUE WHERE conversation_id = ? AND user_id = ?`,
		conversationID, first); err != nil {
		return nil, err
	}
	profiles, err := s.profileMap(ctx, []ids.UUID{first})
	if err != nil {
		return nil, err
	}
	return profiles[first], nil
}

// SetTopic changes a group's topic; any member may.
func (s *Service) SetTopic(ctx context.Context, userID, conversationID ids.UUID, topic string) error {
	if err := CheckTopic(topic); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := group(ctx, tx, selectStateForUpdate, conversationID, userID)
	if err != nil {
		return err
	}
	if st.topic == topic {
		return tx.Commit()
	}
	st.topic = topic
	st.lastSeq++
	notice := &pb.Notice{Kind: &pb.Notice_TopicChanged{TopicChanged: &pb.TopicChanged{Topic: topic}}}
	if err := storeNotice(ctx, tx, conversationID, st, userID, notice, st.members); err != nil {
		return err
	}
	return tx.Commit()
}

// SetAdmin makes a member an admin or no longer one; only admins may. A
// group keeps at least one admin.
func (s *Service) SetAdmin(ctx context.Context, userID, conversationID, targetID ids.UUID, admin bool) error {
	profiles, err := s.profileMap(ctx, []ids.UUID{targetID})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := group(ctx, tx, selectStateForUpdate, conversationID, userID)
	if err != nil {
		return err
	}
	if err := requireAdmin(ctx, tx, conversationID, userID); err != nil {
		return err
	}
	if !st.has(targetID) {
		return apierror.NotFound("user %s is not in group %s", targetID, conversationID)
	}
	current, err := isAdmin(ctx, tx, conversationID, targetID)
	if err != nil {
		return err
	}
	if current == admin {
		return tx.Commit()
	}
	if !admin {
		var admins int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM conversation_member WHERE conversation_id = ? AND left_seq IS NULL AND admin`,
			conversationID).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return apierror.FailedPrecondition("a group keeps at least one admin")
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_member SET admin = ? WHERE conversation_id = ? AND user_id = ?`,
		admin, conversationID, targetID); err != nil {
		return err
	}
	st.lastSeq++
	notice := &pb.Notice{Kind: &pb.Notice_AdminChanged{AdminChanged: &pb.AdminChanged{User: profiles[targetID], Admin: admin}}}
	if err := storeNotice(ctx, tx, conversationID, st, userID, notice, st.members); err != nil {
		return err
	}
	return tx.Commit()
}

// Members returns a group's current members, those who joined first first.
func (s *Service) Members(ctx context.Context, userID, conversationID ids.UUID) ([]*pb.Member, error) {
	if _, err := group(ctx, s.db, selectState, conversationID, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, admin, joined_seq FROM conversation_member
		WHERE conversation_id = ? AND left_seq IS NULL ORDER BY joined_seq, joined_at, user_id`,
		conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []*pb.Member
	var memberIDs []ids.UUID
	for rows.Next() {
		var id ids.UUID
		m := &pb.Member{}
		if err := rows.Scan(&id, &m.Admin, &m.JoinedSeq); err != nil {
			return nil, err
		}
		members = append(members, m)
		memberIDs = append(memberIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	reads, err := s.readMarks(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	profiles, err := s.profileMap(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	for i, m := range members {
		m.User = profiles[memberIDs[i]]
		m.ReadSeq = reads[memberIDs[i]]
	}
	return members, nil
}

func (s *Service) readMarks(ctx context.Context, conversationID ids.UUID) (map[ids.UUID]uint64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, seq FROM read_mark WHERE conversation_id = ?`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reads := map[ids.UUID]uint64{}
	for rows.Next() {
		var id ids.UUID
		var seq uint64
		if err := rows.Scan(&id, &seq); err != nil {
			return nil, err
		}
		reads[id] = seq
	}
	return reads, rows.Err()
}

// Describe returns the conversation as the user sees it now, from the
// conversation keyspace.
func (s *Service) Describe(ctx context.Context, conversationID, userID ids.UUID) (*pb.Conversation, error) {
	st, err := load(ctx, s.db, selectState, conversationID)
	if err != nil {
		return nil, err
	}
	c := &pb.Conversation{
		ConversationId: conversationID.Bytes(),
		Kind:           Kind(st.kind),
		Topic:          st.topic,
		MemberCount:    uint32(len(st.members)),
		LastSeq:        st.lastSeq,
	}
	var leftSeq sql.NullInt64
	err = s.db.QueryRowContext(ctx, `
		SELECT admin, left_seq FROM conversation_member WHERE conversation_id = ? AND user_id = ?`,
		conversationID, userID).Scan(&c.Admin, &leftSeq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	c.Member = err == nil && !leftSeq.Valid
	if st.lastSeq > 0 {
		msg := &pb.Message{ConversationId: c.ConversationId, Seq: st.lastSeq}
		var body, blobs []byte
		var sentAt time.Time
		err := s.db.QueryRowContext(ctx, `
			SELECT sender_user_id, client_message_id, body, blob_ids, sent_at FROM message WHERE conversation_id = ? AND seq = ?`,
			conversationID, st.lastSeq).Scan(&msg.SenderUserId, &msg.ClientMessageId, &body, &blobs, &sentAt)
		switch {
		case err == nil:
			msg.SentAt = timestamppb.New(sentAt)
			msg.BlobIds = decodeBlobIDs(blobs)
			if err := decodeBody(body, msg); err != nil {
				return nil, err
			}
			c.LastMessage = msg
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT seq FROM read_mark WHERE conversation_id = ? AND user_id = ?`,
		conversationID, userID).Scan(&c.ReadSeq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return c, nil
}

// Kind is the contract's kind of a stored one.
func Kind(kind string) pb.ConversationKind {
	switch kind {
	case kindDirect:
		return pb.ConversationKind_CONVERSATION_KIND_DIRECT
	case kindGroup:
		return pb.ConversationKind_CONVERSATION_KIND_GROUP
	case kindNotes:
		return pb.ConversationKind_CONVERSATION_KIND_NOTES
	}
	return pb.ConversationKind_CONVERSATION_KIND_UNSPECIFIED
}

// requireAdmin fails unless the current member is an admin.
func requireAdmin(ctx context.Context, tx *sql.Tx, conversationID, userID ids.UUID) error {
	admin, err := isAdmin(ctx, tx, conversationID, userID)
	if err != nil {
		return err
	}
	if !admin {
		return apierror.PermissionDenied("user %s is not an admin of group %s", userID, conversationID)
	}
	return nil
}

func isAdmin(ctx context.Context, tx *sql.Tx, conversationID, userID ids.UUID) (bool, error) {
	var admin bool
	err := tx.QueryRowContext(ctx, `
		SELECT admin FROM conversation_member WHERE conversation_id = ? AND user_id = ? AND left_seq IS NULL`,
		conversationID, userID).Scan(&admin)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return admin, err
}

// leftAt ends the user's membership at the notice with seq.
func (s *Service) leftAt(ctx context.Context, tx *sql.Tx, conversationID, userID ids.UUID, seq uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE conversation_member SET left_seq = ?, admin = FALSE WHERE conversation_id = ? AND user_id = ?`,
		seq, conversationID, userID)
	return err
}

func without(members []ids.UUID, userID ids.UUID) []ids.UUID {
	return slices.DeleteFunc(slices.Clone(members), func(id ids.UUID) bool { return id == userID })
}

// profileMap reads the users' profiles as they are now.
func (s *Service) profileMap(ctx context.Context, userIDs []ids.UUID) (map[ids.UUID]*pb.User, error) {
	return s.profiles.Profiles(ctx, userIDs)
}

// snapshots returns the users' profiles in the order given.
func (s *Service) snapshots(ctx context.Context, userIDs []ids.UUID) ([]*pb.User, error) {
	profiles, err := s.profileMap(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	users := make([]*pb.User, len(userIDs))
	for i, id := range userIDs {
		users[i] = profiles[id]
	}
	return users, nil
}
