package wrinkleclient

import (
	"context"
	"fmt"
	"slices"

	"github.com/vlumi/wrinkle/clients/go/e2ee"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	maxReactions     = 3
	maxReactionBytes = 32
)

// SetReactions seals the user's reactions to a message with the
// conversation's current epoch and sends them, replacing earlier ones; no
// emoji removes them. A set refused for its epoch is retried once after
// checking the keys again.
func (c *Client) SetReactions(ctx context.Context, conversationID []byte, seq uint64, emoji []string) error {
	if err := checkReactions(emoji); err != nil {
		return err
	}
	if len(emoji) == 0 {
		return c.SetReaction(ctx, conversationID, seq, nil)
	}
	k, err := c.requireKeys()
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		epoch, key, err := k.sendKey(ctx, c, conversationID)
		if err != nil {
			return err
		}
		sealed, err := e2ee.SealReactions(key, e2ee.ReactionContext{
			ConversationID: conversationID, Epoch: epoch, UserID: k.UserID, Seq: seq,
		}, &pb.Reactions{Emoji: emoji})
		if err != nil {
			return err
		}
		err = c.SetReaction(ctx, conversationID, seq, sealed)
		if Code(err) == pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION && attempt == 0 {
			k.Invalidate(conversationID)
			continue
		}
		return err
	}
}

// checkReactions holds a set to the contract's limits, counting bytes
// rather than grapheme clusters.
func checkReactions(emoji []string) error {
	if len(emoji) > maxReactions {
		return fmt.Errorf("at most %d reactions", maxReactions)
	}
	for i, e := range emoji {
		if e == "" || len(e) > maxReactionBytes {
			return fmt.Errorf("a reaction must be 1 to %d bytes", maxReactionBytes)
		}
		if slices.Contains(emoji[:i], e) {
			return fmt.Errorf("reaction %q twice", e)
		}
	}
	return nil
}

// OpenReactions opens a user's sealed reactions to a message, fetching the
// key of their epoch if this device does not have it yet.
func (c *Client) OpenReactions(ctx context.Context, conversationID []byte, seq uint64, userID []byte, sealed *pb.Sealed) ([]string, error) {
	k, err := c.requireKeys()
	if err != nil {
		return nil, err
	}
	if sealed == nil {
		return nil, ErrNotSealed
	}
	key, err := k.epochKey(ctx, c, conversationID, sealed.GetEpoch())
	if err != nil {
		return nil, err
	}
	r, err := e2ee.OpenReactions(key, e2ee.ReactionContext{
		ConversationID: conversationID, Epoch: sealed.GetEpoch(), UserID: userID, Seq: seq,
	}, sealed)
	if err != nil {
		return nil, err
	}
	return r.GetEmoji(), nil
}
