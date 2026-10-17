#!/usr/bin/env python3
"""Pull older history for one or more chats until a target date is reached.

WhatsApp only sends a limited window of past messages when the bridge first connects. Each
call to the bridge's GET /api/backfill asks the phone for messages older than the oldest one
already stored for a chat; the bridge writes the reply to messages.db. This script repeats
that request until the oldest stored message is on or before the target date, or the phone
stops returning anything older.

Safety model:
  - messages.db is opened READ-ONLY (sqlite URI mode=ro); this script never writes to it.
  - The only network call is to the local bridge. The bridge talks to your own phone.

Usage:
  python3 scripts/backfill.py 2026-03-01 972527872233@s.whatsapp.net
  python3 scripts/backfill.py 2026-01-01 120363239970576980@g.us --count 500
  STORE_DIR=/srv/whatsapp/store python3 scripts/backfill.py 2026-03-01 972527872233@s.whatsapp.net
"""

import argparse
import json
import os
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


def default_db_path() -> str:
    store_dir = os.environ.get("STORE_DIR")
    if not store_dir:
        repo_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        store_dir = os.path.join(repo_root, "whatsapp-bridge", "store")
    return os.path.join(store_dir, "messages.db")


def oldest(db_path: str, chat: str):
    if not os.path.isfile(db_path):
        sys.exit(f"database not found: {db_path}")
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True, timeout=5)
    conn.execute("PRAGMA query_only = ON")
    row = conn.execute(
        "SELECT min(timestamp), count(*) FROM messages WHERE chat_jid = ? AND id != ''", (chat,)
    ).fetchone()
    conn.close()
    return (row[0] or ""), row[1]


def request_backfill(bridge: str, chat: str, count: int) -> dict:
    url = bridge.rstrip("/") + "/api/backfill?" + urllib.parse.urlencode({"chat": chat, "count": count})
    with urllib.request.urlopen(url, timeout=90) as response:
        return json.loads(response.read())


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("target_date", help="stop once the oldest stored message is on or before this date (YYYY-MM-DD)")
    parser.add_argument("chats", nargs="+", help="chat JIDs, e.g. 972527872233@s.whatsapp.net or 1203...@g.us")
    parser.add_argument("--count", type=int, default=200, help="messages to request per round (default 200)")
    parser.add_argument("--bridge", default="http://localhost:8080", help="bridge base URL (default http://localhost:8080)")
    parser.add_argument("--db", default=default_db_path(), help="path to messages.db (default: $STORE_DIR or whatsapp-bridge/store)")
    parser.add_argument("--wait", type=float, default=12.0, help="seconds to wait for the phone after each request (default 12)")
    args = parser.parse_args()

    for chat in args.chats:
        stale = 0
        while True:
            before, n_before = oldest(args.db, chat)
            if not before:
                print(f"{chat}: nothing stored yet; the bridge needs at least one message to start from")
                break
            if before[:10] <= args.target_date:
                print(f"{chat}: reached {before[:10]} ({n_before} messages)")
                break
            try:
                request_backfill(args.bridge, chat, args.count)
            except urllib.error.HTTPError as e:
                print(f"{chat}: bridge returned {e.code}: {e.read().decode(errors='replace')[:200]}")
                break
            except Exception as e:  # connection refused, timeout
                print(f"{chat}: request failed: {e}")
                break
            time.sleep(args.wait)
            after, n_after = oldest(args.db, chat)
            print(f"{chat}: oldest {before[:19]} -> {after[:19]} (+{n_after - n_before} messages)", flush=True)
            if n_after == n_before:
                stale += 1
                if stale >= 2:
                    print(f"{chat}: no more history available (stopped at {after[:10]})")
                    break
            else:
                stale = 0


if __name__ == "__main__":
    main()
