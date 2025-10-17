#!/bin/bash

set -e

usage() {
    echo "❌ Usage: $0 \"migration description\""
    exit 1
}

[ -z "$1" ] && usage

MIGRATION_NAME="${1// /_}"  # Replace spaces with underscores
MIGRATION_DIR="migrations"
mkdir -p "$MIGRATION_DIR"

# Find highest migration number
MIGRATION_NUMBER=$(find "$MIGRATION_DIR" -name "*.up.sql" 2>/dev/null | \
    sed 's/.*\/\([0-9]*\)_.*/\1/' | \
    sort -n | \
    tail -1)

MIGRATION_NUMBER=${MIGRATION_NUMBER:-0}
MIGRATION_NUMBER=$((MIGRATION_NUMBER + 1))

PREFIX="${MIGRATION_NUMBER}_${MIGRATION_NAME}"
UP_FILE="${MIGRATION_DIR}/${PREFIX}.up.sql"
DOWN_FILE="${MIGRATION_DIR}/${PREFIX}.down.sql"

create_file() {
    local file=$1 type=$2
    cat > "$file" << EOF
-- Migration: $MIGRATION_NAME
-- Created: $(date)
-- Type: $type

-- Add your SQL commands here
EOF
}

create_file "$UP_FILE" "UP"
create_file "$DOWN_FILE" "DOWN"

echo "✅ Migration created:"
echo "   📄 Up:   ${UP_FILE##*/}"
echo "   📄 Down: ${DOWN_FILE##*/}"
echo "📝 Edit files to add SQL commands"