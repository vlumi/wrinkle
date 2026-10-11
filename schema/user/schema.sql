-- Keyspace "user", sharded by user_id. A user's event log, conversation list
-- and devices live on one shard, so a sync is one local read. User ids are
-- random UUIDs, 16 bytes.

CREATE TABLE user (
  user_id BINARY(16) NOT NULL,
  -- As the user typed it; user_name holds the claim that keeps it unique.
  name VARCHAR(64) NOT NULL,
  -- A standard icon key, empty for none.
  icon VARCHAR(32) NOT NULL DEFAULT '',
  -- argon2id in the PHC string format.
  password_hash VARCHAR(255) NOT NULL,
  -- Position counter of the event log; the next event gets log_head + 1.
  log_head BIGINT UNSIGNED NOT NULL DEFAULT 0,
  -- Outbox id counter for this user's profile changes.
  last_outbox_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL,
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

-- The per-user event log. Devices read it by position.
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
  UNIQUE KEY user_event_source (user_id, source_id, outbox_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- The user's conversation list, maintained by fan-out workers.
CREATE TABLE user_conversation (
  user_id BINARY(16) NOT NULL,
  conversation_id BINARY(16) NOT NULL,
  peer_user_id BINARY(16) NOT NULL,
  -- The peer's profile, updated by their profile changes.
  peer_name VARCHAR(64) NOT NULL,
  peer_icon VARCHAR(32) NOT NULL DEFAULT '',
  last_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  -- The last message, NULL while the conversation is empty.
  last_sender_user_id BINARY(16) NULL,
  last_client_message_id VARCHAR(64) NULL,
  last_body TEXT NULL,
  last_sent_at DATETIME(6) NULL,
  read_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  peer_read_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, conversation_id),
  KEY user_conversation_recent (user_id, updated_at),
  KEY user_conversation_peer (user_id, peer_user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Transactional outbox of profile changes, like the conversation keyspace's:
-- each row is deleted in the transaction that inserts it, and Debezium
-- streams the insert to Kafka. Fan-out appends a ProfileEvent to the user's
-- log and to each conversation partner's.
CREATE TABLE user_outbox (
  user_id BINARY(16) NOT NULL,
  -- From user.last_outbox_id.
  outbox_id BIGINT UNSIGNED NOT NULL,
  -- Event body type: profile.
  kind VARCHAR(32) NOT NULL,
  -- Serialized wrinkle.v1.Event without a cursor.
  payload BLOB NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, outbox_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
