#!/usr/bin/env bash
# Runs the webhook UI's jsdom tests in a Node container, so nothing is installed on the host
# (which has no Node at all).
#
#   scripts/test-ui.sh
#
# Runs as the calling user, so node_modules is not left root-owned in the working tree.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"

docker run --rm \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp \
  -e npm_config_cache=/tmp/.npm \
  -v "$root/whatsapp-webhook-ui:/ui" \
  -w /ui/test \
  node:22-slim \
  sh -c 'npm ci --no-audit --no-fund --loglevel=error && npm test'
