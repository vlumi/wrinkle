-- Keyspace "user", sharded by user_id. A user's event log, conversation list
-- and devices live on one shard, so a sync is one local read. User ids are
-- random UUIDs, 16 bytes.

CREATE TABLE user (
  user_id BINARY(16) NOT NULL,
  -- As the user typed it; user_name holds the claim that keeps it unique.
  name VARCHAR(64) NOT NULL,
  -- A standard icon key, empty for none.
  icon VARCHAR(32) NOT NULL DEFAULT '',
  -- An uploaded picture's blob id, NULL for none.
  avatar BINARY(16) NULL,
  -- argon2id in the PHC string format; empty once the account is deleted.
  password_hash VARCHAR(255) NOT NULL,
  -- Position counter of the event log; the next event gets log_head + 1.
  log_head BIGINT UNSIGNED NOT NULL DEFAULT 0,
  -- The newest position dropped from the log by its retention: events up to
  -- it are gone, and a sync from before it has expired. Raised in the
  -- transaction that drops them.
  log_tail BIGINT UNSIGNED NOT NULL DEFAULT 0,
  -- Outbox id counter for this user's profile changes.
  last_outbox_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL,
  -- Set when the account is deleted; the row stays so that nothing is
  -- delivered to it again.
  deleted_at DATETIME(6) NULL,
  PRIMARY KEY (user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Name claims, sharded by name, case-insensitively. A name belongs to the
-- user whose row it names only while that user's name still equals it,
-- ignoring case; a claim left behind by an interrupted rename or
-- registration is stale and may be taken over.
CREATE TABLE user_name (
  name VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_as_ci NOT NULL,
  user_id BINARY(16) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (name)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

CREATE TABLE device (
  user_id BINARY(16) NOT NULL,
  device_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(64) NOT NULL,
  -- SHA-256 of the secret part of the device token; NULL once signed out.
  token_hash BINARY(32) NULL,
  -- The device's X25519 public key, once set.
  public_key VARBINARY(32) NULL,
  -- The log position the device last acked.
  acked_position BIGINT UNSIGNED NOT NULL DEFAULT 0,
  -- Where to push while the device is not connected: the PushService name
  -- and the token it issued; NULL when the device has not registered.
  push_service VARCHAR(32) NULL,
  push_token VARCHAR(200) NULL,
  created_at DATETIME(6) NOT NULL,
  last_seen_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, device_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- The per-user event log. Devices read it by position. Events older than
-- the retention are dropped, oldest first, through the index on their age.
CREATE TABLE user_event (
  user_id BINARY(16) NOT NULL,
  position BIGINT UNSIGNED NOT NULL,
  -- The outbox row this event came from: a conversation's or a user's, by
  -- its id; makes fan-out idempotent.
  source_id BINARY(16) NOT NULL,
  outbox_id BIGINT UNSIGNED NOT NULL,
  -- Serialized wrinkle.v1.Event, cursor set to position.
  payload BLOB NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, position),
  UNIQUE KEY user_event_source (user_id, source_id, outbox_id),
  KEY user_event_age (occurred_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- The user's conversation list, maintained by fan-out workers.
CREATE TABLE user_conversation (
  user_id BINARY(16) NOT NULL,
  conversation_id BINARY(16) NOT NULL,
  -- direct, group or notes.
  kind VARCHAR(8) NOT NULL,
  -- Direct conversations: the peer and their profile, updated by their
  -- profile changes. NULL and empty for groups.
  peer_user_id BINARY(16) NULL,
  peer_name VARCHAR(64) NOT NULL DEFAULT '',
  peer_icon VARCHAR(32) NOT NULL DEFAULT '',
  peer_avatar BINARY(16) NULL,
  peer_deleted BOOLEAN NOT NULL DEFAULT FALSE,
  -- Groups: as of the latest change, and the user's place in it.
  topic VARCHAR(255) NOT NULL DEFAULT '',
  member_count INT UNSIGNED NOT NULL DEFAULT 0,
  member BOOLEAN NOT NULL DEFAULT TRUE,
  admin BOOLEAN NOT NULL DEFAULT FALSE,
  last_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  -- The last message, NULL while the conversation is empty.
  last_sender_user_id BINARY(16) NULL,
  last_client_message_id VARCHAR(64) NULL,
  -- As message.body, deleted messages included, with the message's
  -- attachment blob ids too.
  last_body BLOB NULL,
  last_sent_at DATETIME(6) NULL,
  read_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  -- Direct conversations: the peer's read position.
  peer_read_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, conversation_id),
  KEY user_conversation_recent (user_id, updated_at),
  KEY user_conversation_peer (user_id, peer_user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Transactional outbox of a user's own changes, like the conversation
-- keyspace's: each row is deleted in the transaction that inserts it, and
-- Debezium streams the insert to Kafka. Fan-out appends a ProfileEvent to
-- the user's log and to each conversation partner's, a DeviceEvent to the
-- user's log and, for a device with a key, to the partners and the members
-- of the user's groups.
CREATE TABLE user_outbox (
  user_id BINARY(16) NOT NULL,
  -- From user.last_outbox_id.
  outbox_id BIGINT UNSIGNED NOT NULL,
  -- Event body type: profile, device, palette, contact.
  kind VARCHAR(32) NOT NULL,
  -- Serialized wrinkle.v1.Event without a cursor.
  payload BLOB NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, outbox_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Stickers, public and permanent, sharded by their id (their blob's id). A
-- sticker outlives its creator's account.
CREATE TABLE sticker (
  sticker_id BINARY(16) NOT NULL,
  creator_user_id BINARY(16) NOT NULL,
  media_type VARCHAR(100) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (sticker_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Each user's saved stickers, in their order.
CREATE TABLE palette_sticker (
  user_id BINARY(16) NOT NULL,
  sticker_id BINARY(16) NOT NULL,
  position INT UNSIGNED NOT NULL,
  saved_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, sticker_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Uploaded blobs, by uploader. The bytes are in object storage under the
-- blob id. A blob is used once a profile, sticker or message refers to it;
-- unused ones are deleted a day after their upload slot was made, and a
-- blob no longer referred to goes back to unused.
CREATE TABLE upload (
  user_id BINARY(16) NOT NULL,
  blob_id BINARY(16) NOT NULL,
  -- avatar, sticker, attachment.
  purpose VARCHAR(16) NOT NULL,
  media_type VARCHAR(100) NOT NULL,
  size BIGINT UNSIGNED NOT NULL,
  used BOOLEAN NOT NULL DEFAULT FALSE,
  -- An attachment a send took into use, until the fan-out delivers the
  -- message; one still pending a day later is checked against the message
  -- it was taken for, and deleted if the send failed.
  pending_since DATETIME(6) NULL,
  conversation_id BINARY(16) NULL,
  client_message_id VARCHAR(64) NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, blob_id),
  KEY upload_unused (used, created_at),
  KEY upload_pending (pending_since)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Each user's own side toward other users: a request, which is also how one
-- accepts, or a block. Only the user's own requests change it, through the
-- user outbox; the fan-out copies it to the other user's contact_peer and,
-- when it ends a request of the other user's, ends that one through the
-- other user's outbox. Blocks are checked against this table, on the
-- blocking user's shard.
CREATE TABLE contact (
  user_id BINARY(16) NOT NULL,
  other_user_id BINARY(16) NOT NULL,
  -- request, block.
  state VARCHAR(8) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, other_user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- The other user's side toward this user, as the fan-out last copied it
-- from their contact row. Lists incoming requests without reading every
-- shard.
CREATE TABLE contact_peer (
  user_id BINARY(16) NOT NULL,
  other_user_id BINARY(16) NOT NULL,
  -- request, block, or none once it ended (kept for source_outbox_id).
  state VARCHAR(8) NOT NULL,
  -- The other user's outbox_id of the change copied, so a redelivered or
  -- older change is ignored.
  source_outbox_id BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, other_user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
