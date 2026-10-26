#!/bin/bash
# Checks what must hold of the stored data once the fan-out has caught up,
# as after a load or conformance run: dense sequences, contiguous logs,
# conversation lists that match their conversations, and settled uploads.
# Retries for up to WAIT seconds (default 30) while the fan-out catches up;
# exits 1 with the failing checks after that.
set -euo pipefail
cd "$(dirname "$0")"
profile=$(./profile.sh)
case $profile in
  vitess) sql() { docker compose --profile vitess exec -T vitess mysql -h 127.0.0.1 -P 15003 -N -e "$1"; } ;;
  mysql) sql() { docker compose --profile mysql exec -T mysql mysql -u root -N -e "$1"; } ;;
esac

checks=(
  "every seq after the watermark has its message in MySQL, and none at or below it"
  "SELECT COUNT(*) FROM (SELECT c.conversation_id FROM conversation.conversation c
     LEFT JOIN conversation.message m ON m.conversation_id = c.conversation_id
     GROUP BY c.conversation_id, c.last_seq, c.cold_seq
     HAVING COUNT(m.seq) <> c.last_seq - c.cold_seq OR MIN(m.seq) <= c.cold_seq OR MAX(m.seq) <> c.last_seq) AS x"

  "no reaction to a message at or below the watermark is left in MySQL"
  "SELECT COUNT(*) FROM conversation.reaction r JOIN conversation.conversation c ON c.conversation_id = r.conversation_id
     WHERE r.seq <= c.cold_seq"

  "no read position beyond the conversation's last message"
  "SELECT COUNT(*) FROM conversation.read_mark r JOIN conversation.conversation c ON c.conversation_id = r.conversation_id
     WHERE r.seq > c.last_seq"

  "every user's log holds exactly the positions after its tail up to its head"
  "SELECT COUNT(*) FROM (SELECT u.user_id FROM user.user u LEFT JOIN user.user_event e ON e.user_id = u.user_id
     WHERE u.deleted_at IS NULL GROUP BY u.user_id, u.log_head, u.log_tail
     HAVING COUNT(e.position) <> u.log_head - u.log_tail OR MIN(e.position) <= u.log_tail OR MAX(e.position) <> u.log_head) AS x"

  "every current member's list entry is at the conversation's last message"
  "SELECT COUNT(*) FROM user.user_conversation uc
     JOIN conversation.conversation_member cm ON cm.conversation_id = uc.conversation_id AND cm.user_id = uc.user_id
     JOIN conversation.conversation c ON c.conversation_id = uc.conversation_id
     WHERE cm.left_seq IS NULL AND uc.last_seq <> c.last_seq"

  "no attachment upload is still pending a minute after its send"
  "SELECT COUNT(*) FROM user.upload WHERE pending_since < UTC_TIMESTAMP(6) - INTERVAL 1 MINUTE"
)

deadline=$(($(date +%s) + ${WAIT:-30}))
while :; do
  failed=()
  for ((i = 0; i < ${#checks[@]}; i += 2)); do
    n=$(sql "${checks[i + 1]}")
    if [ "$n" != 0 ]; then failed+=("${checks[i]}: $n violations"); fi
  done
  if [ ${#failed[@]} -eq 0 ]; then
    echo "invariants hold ($profile, $((${#checks[@]} / 2)) checks)"
    exit 0
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    printf 'invariant broken: %s\n' "${failed[@]}" >&2
    exit 1
  fi
  sleep 2
done
