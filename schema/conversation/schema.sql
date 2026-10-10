-- Keyspace "conversation", sharded by conversation_id. Everything about one
-- conversation lives on one shard, so a send is one local transaction.

CREATE TABLE conversation (
  conversation_id BIGINT UNSIGNED NOT NULL,
  -- The two participants, user_a < user_b.
  user_a BIGINT UNSIGNED NOT NULL,
  user_b BIGINT UNSIGNED NOT NULL,
  -- Sequence counter; the next message gets last_seq + 1.
  last_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (conversation_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

CREATE TABLE message (
  conversation_id BIGINT UNSIGNED NOT NULL,
  seq BIGINT UNSIGNED NOT NULL,
  sender_user_id BIGINT UNSIGNED NOT NULL,
  client_message_id VARCHAR(64) NOT NULL,
  body TEXT NOT NULL,
  sent_at DATETIME(6) NOT NULL,
  PRIMARY KEY (conversation_id, seq),
  -- Resending with the same client id finds the stored message.
  UNIQUE KEY message_client_id (conversation_id, sender_user_id, client_message_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

CREATE TABLE read_mark (
  conversation_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  seq BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Transactional outbox. Each change to a conversation inserts one row in the
-- same transaction; Debezium streams the rows to Kafka, and fan-out workers
-- append them to the participants' event logs.
CREATE TABLE outbox (
  conversation_id BIGINT UNSIGNED NOT NULL,
  -- Unique within the conversation; assigned by the writer.
  outbox_id BIGINT UNSIGNED NOT NULL,
  -- Participants, so a worker needs no lookup: user_a < user_b.
  user_a BIGINT UNSIGNED NOT NULL,
  user_b BIGINT UNSIGNED NOT NULL,
  -- Event body type: message, read, message_failed.
  kind VARCHAR(32) NOT NULL,
  -- Serialized wrinkle.v1.Event without a cursor; each recipient's log
  -- assigns its own.
  payload BLOB NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (conversation_id, outbox_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
