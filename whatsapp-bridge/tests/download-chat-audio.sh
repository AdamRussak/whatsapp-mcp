#!/usr/bin/env bash
set -euo pipefail

CHAT="${1:-}"
if [ -z "$CHAT" ]; then
    echo "Usage: $0 <phone-number-or-jid> [limit]"
    echo "Example: $0 972527872233 100"
    exit 1
fi

LIMIT="${2:-5000}"
AFTER="${3:-${AFTER:-}}"
BEFORE="${4:-${BEFORE:-}}"
SKIP_MEDIA_RETRY="${SKIP_MEDIA_RETRY:-true}"
BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"

echo "=================================================="
echo "Starting chat audio download for: ${CHAT}"
echo "Bridge URL: ${BRIDGE_URL}"
echo "Limit: ${LIMIT}"
if [ -n "${AFTER}" ]; then echo "After: ${AFTER}"; fi
if [ -n "${BEFORE}" ]; then echo "Before: ${BEFORE}"; fi
echo "Skip media retry: ${SKIP_MEDIA_RETRY}"
echo "=================================================="

# 1. Trigger the batch job
PAYLOAD=$(jq -n \
    --arg chat "$CHAT" \
    --argjson limit "$LIMIT" \
    --arg after "$AFTER" \
    --arg before "$BEFORE" \
    --argjson skip_media_retry "$SKIP_MEDIA_RETRY" \
    '{
        chat: $chat,
        limit: $limit,
        skip_cached: true,
        skip_media_retry: $skip_media_retry
    } + (if $after != "" then {after: $after} else {} end) + (if $before != "" then {before: $before} else {} end)')

RESP=$(curl -s -f -X POST "${BRIDGE_URL}/api/download/batch" \
    -H "Content-Type: application/json" \
    -d "${PAYLOAD}")

JOB_ID=$(echo "${RESP}" | jq -r '.job_id // empty')
TOTAL=$(echo "${RESP}" | jq -r '.total // 0')
RESOLVED_JIDS=$(echo "${RESP}" | jq -r '.resolved_jids | join(", ") // ""')
FOLDER=$(echo "${RESP}" | jq -r '.folder // empty')

if [ -z "$JOB_ID" ]; then
    echo "Failed to start batch job. Response:"
    echo "${RESP}"
    exit 1
fi

echo "Job started successfully!"
echo "Job ID:        ${JOB_ID}"
echo "Resolved JIDs: ${RESOLVED_JIDS}"
echo "Folder:        store/${FOLDER}"
echo "Total items:   ${TOTAL}"
echo "--------------------------------------------------"

if [ "$TOTAL" -eq 0 ]; then
    echo "No audio messages found for ${CHAT}."
    exit 0
fi

# 2. Poll job status until completion
while true; do
    sleep 5
    STATUS_RESP=$(curl -s -f "${BRIDGE_URL}/api/download/batch/${JOB_ID}" || echo "")
    if [ -z "$STATUS_RESP" ]; then
        echo "Warning: failed to fetch job status, retrying..."
        continue
    fi

    STATUS=$(echo "${STATUS_RESP}" | jq -r '.status // "unknown"')
    DONE=$(echo "${STATUS_RESP}" | jq -r '.done // 0')
    FAILED=$(echo "${STATUS_RESP}" | jq -r '.failed // 0')
    SKIPPED=$(echo "${STATUS_RESP}" | jq -r '.skipped // 0')
    CURR_MSG=$(echo "${STATUS_RESP}" | jq -r '.current_message_id // ""')

    PROCESSED=$((DONE + FAILED + SKIPPED))
    PERCENT=0
    if [ "$TOTAL" -gt 0 ]; then
        PERCENT=$((PROCESSED * 100 / TOTAL))
    fi

    printf "Progress: [%3d%%] %d/%d (done: %d, failed: %d, skipped/cached: %d) curr: %s\n" \
        "${PERCENT}" "${PROCESSED}" "${TOTAL}" "${DONE}" "${FAILED}" "${SKIPPED}" "${CURR_MSG}"

    if [ "$STATUS" = "completed" ] || [ "$STATUS" = "failed" ] || [ "$STATUS" = "cancelled" ]; then
        echo "--------------------------------------------------"
        echo "Job finished with status: ${STATUS}"
        echo "Summary: done=${DONE}, failed=${FAILED}, skipped=${SKIPPED}, total=${TOTAL}"
        break
    fi
done

# 3. Print output directory and index.json location (folder as reported by the bridge)
echo "Output files saved under store/${FOLDER}/"
if [ -n "${FOLDER}" ] && [ -f "store/${FOLDER}/index.json" ]; then
    echo "Index file: store/${FOLDER}/index.json"
fi
