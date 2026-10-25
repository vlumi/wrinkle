# The cold tier of the conversation history, for the HBase shell: messages
# at or below their conversation's cold_seq, moved out of MySQL.
#
# Row key: conversation_id (16 bytes) followed by seq (8 bytes, big-endian),
# so a conversation's history is one contiguous range, in order. Random
# conversation ids spread the rows; the table is pre-split on their first
# byte.
#
# Family m, one cell per column of the conversation keyspace's message
# table, and the reactions:
#   sender      sender_user_id, 16 bytes
#   client_id   client_message_id, UTF-8
#   body        the serialized wrinkle.v1.Message, as in MySQL
#   sent_at     microseconds since the epoch, 8 bytes big-endian
#   outbox_id   8 bytes big-endian
#   blob_ids    16 bytes each, empty once deleted
#   reactions   a serialized wrinkle.v1.Message with only its reactions,
#               oldest first; empty for none
# Every write puts the whole row.

unless list.include?('message')
  create 'message',
    { NAME => 'm', VERSIONS => 1, DATA_BLOCK_ENCODING => 'FAST_DIFF' },
    SPLITS => (1..15).map { |i| (i << 4).chr }
end
