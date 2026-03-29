#!/bin/bash
# Truncate all chat tables (messages, read receipts) inside the jarvis_memory container.
# Usage: bash scripts/clear-chat.sh

set -euo pipefail

CONTAINER="jarvis_memory"
DB_USER="${POSTGRES_USER:-postgres}"
DB_NAME="${POSTGRES_DB:-postgres}"

echo "Clearing chat records in ${CONTAINER}..."

docker exec -i "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" <<'SQL'
BEGIN;
TRUNCATE chat_read_receipts, chat_messages RESTART IDENTITY CASCADE;
UPDATE chat_rooms SET last_msg_text = NULL, last_msg_at = NULL, last_msg_by = NULL;
COMMIT;
SQL

echo "Done. All chat messages and read receipts have been cleared."
