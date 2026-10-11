-- Keyspace "conversation", sharded by conversation_id. Everything about one
-- conversation lives on one shard, so a send is one local transaction.
-- Conversation and user ids are random UUIDs, 16 bytes.

CREATE TABLE conversation (
  conversation_id BINARY(16) NOT NULL,
  -- direct or group, fixed at creation.
  kind VARCHAR(8) NOT NULL,
  -- Groups only; empty for direct conversations.
  topic VARCHAR(255) NOT NULL DEFAULT '',
  -- The current members' ids, 16 bytes each, sorted bytewise: what a send
  -- checks and fans out to, read with the row it locks anyway. A group has
  -- at most 256 members.
  members VARBINARY(4096) NOT NULL,
  -- Sequence counter; the next message gets last_seq + 1.
  last_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (conversation_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Everyone who is or was a member, with their role and the part of the
-- sequence they may read. Direct conversations have their two
-- participants here too.
CREATE TABLE conversation_member (
  conversation_id BINARY(16) NOT NULL,
  user_id BINARY(16) NOT NULL,
  admin BOOLEAN NOT NULL DEFAULT FALSE,
  -- The seq of the notice that added the member (1 for the creator and
  -- direct participants); the member reads from here on.
  joined_seq BIGINT UNSIGNED NOT NULL,
  -- The seq of the notice that removed the member; NULL while a member.
  left_seq BIGINT UNSIGNED NULL,
  joined_at DATETIME(6) NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- The direct conversation between two users, sharded by user_a, so that
-- both opening it at once find one: the first insert claims the pair. A
-- claim whose conversation row is missing (the opener stopped in between)
-- is completed by the next open.
CREATE TABLE conversation_pair (
  user_a BINARY(16) NOT NULL,
  user_b BINARY(16) NOT NULL,
  conversation_id BINARY(16) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (user_a, user_b)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- A group by its creator's client id, sharded by the creator, so that a
-- retried CreateGroup finds the group created the first time. Completed
-- like a pair claim.
CREATE TABLE group_claim (
  creator_user_id BINARY(16) NOT NULL,
  client_group_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  conversation_id BINARY(16) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (creator_user_id, client_group_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

CREATE TABLE message (
  conversation_id BINARY(16) NOT NULL,
  seq BIGINT UNSIGNED NOT NULL,
  sender_user_id BINARY(16) NOT NULL,
  client_message_id VARCHAR(64) NOT NULL,
  -- The content: serialized wrinkle.v1.Message with only its content set.
  body BLOB NOT NULL,
  sent_at DATETIME(6) NOT NULL,
  -- The outbox row that carried the message, which identifies its copies
  -- in the members' logs.
  outbox_id BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (conversation_id, seq),
  -- Resending with the same client id finds the stored message.
  UNIQUE KEY message_client_id (conversation_id, sender_user_id, client_message_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

CREATE TABLE read_mark (
  conversation_id BINARY(16) NOT NULL,
  user_id BINARY(16) NOT NULL,
  seq BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Transactional outbox. Each change to a conversation inserts one row in the
-- same transaction; Debezium streams the rows to Kafka, and fan-out workers
-- append them to the recipients' event logs.
CREATE TABLE outbox (
  conversation_id BINARY(16) NOT NULL,
  -- Unique within the conversation; assigned by the writer.
  outbox_id BIGINT UNSIGNED NOT NULL,
  -- Who gets the event, 16 bytes each, so a worker needs no lookup.
  recipients VARBINARY(8192) NOT NULL,
  -- The conversation as of the change, for the recipients' lists.
  conversation_kind VARCHAR(8) NOT NULL,
  topic VARCHAR(255) NOT NULL DEFAULT '',
  member_count INT UNSIGNED NOT NULL,
  -- Event body type: message, read.
  kind VARCHAR(32) NOT NULL,
  -- Serialized wrinkle.v1.Event without a cursor; each recipient's log
  -- assigns its own.
  payload BLOB NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (conversation_id, outbox_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
