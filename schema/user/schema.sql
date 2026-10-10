-- Keyspace "user", sharded by user_id. A user's event log, conversation list
-- and devices live on one shard, so a sync is one local read.

CREATE TABLE user (
  user_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(64) NOT NULL,
  -- Position counter of the event log; the next event gets log_head + 1.
  log_head BIGINT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

CREATE TABLE device (
  user_id BIGINT UNSIGNED NOT NULL,
  device_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(64) NOT NULL,
  -- The log position the device last acked.
  acked_position BIGINT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL,
  last_seen_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, device_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- The per-user event log. Devices read it by position.
CREATE TABLE user_event (
  user_id BIGINT UNSIGNED NOT NULL,
  position BIGINT UNSIGNED NOT NULL,
  -- The outbox row this event came from; makes fan-out idempotent.
  conversation_id BIGINT UNSIGNED NOT NULL,
  outbox_id BIGINT UNSIGNED NOT NULL,
  -- Serialized wrinkle.v1.Event, cursor set to position.
  payload BLOB NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, position),
  UNIQUE KEY user_event_source (user_id, conversation_id, outbox_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- The user's conversation list, maintained by fan-out workers.
CREATE TABLE user_conversation (
  user_id BIGINT UNSIGNED NOT NULL,
  conversation_id BIGINT UNSIGNED NOT NULL,
  peer_user_id BIGINT UNSIGNED NOT NULL,
  peer_name VARCHAR(64) NOT NULL,
  last_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  -- The last message, NULL while the conversation is empty.
  last_sender_user_id BIGINT UNSIGNED NULL,
  last_client_message_id VARCHAR(64) NULL,
  last_body TEXT NULL,
  last_sent_at DATETIME(6) NULL,
  read_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  peer_read_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_id, conversation_id),
  KEY user_conversation_recent (user_id, updated_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
