#!/usr/bin/env python3
"""Download and decrypt WhatsApp audio/voice messages for one chat, straight from the CDN.

Safety model:
  - messages.db and whatsapp.db are opened READ-ONLY (sqlite URI mode=ro) and closed
    before any network I/O. This script never writes to either database.
  - It does not talk to WhatsApp's servers as a client and never touches the session.
    It only fetches the encrypted blob from the stored CDN URL and decrypts it locally
    with the media key that is already in messages.db.
  - Output goes to a new folder; files are written to <name>.part and atomically renamed.

Limitation: the CDN only keeps media for a limited time (in practice ~1-2 weeks). The
`oe=` expiry in the URL is NOT reliable in either direction (links past `oe` can still
work, links before it can already be gone), so every URL is tried. Items the CDN has
dropped (403/404/410) are reported; recovering them needs the bridge's media-retry flow.

Usage:
  sudo python3 scripts/download_chat_audio.py 972527872233
  sudo python3 scripts/download_chat_audio.py 972527872233 --limit 1        # test one
  sudo python3 scripts/download_chat_audio.py 120363239970576980@g.us --dry-run
"""

import argparse
import hashlib
import hmac
import json
import os
import re
import sqlite3
import sys
import time
import urllib.error
import urllib.request

from cryptography.hazmat.primitives import hashes, padding
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

AUDIO_HKDF_INFO = b"WhatsApp Audio Keys"
USER_AGENT = "WhatsApp/2.24 Mozilla/5.0"


def ro_connect(path: str) -> sqlite3.Connection:
    """Open SQLite read-only; any write attempt raises instead of touching the file."""
    if not os.path.isfile(path):
        sys.exit(f"database not found: {path}")
    conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=5)
    conn.execute("PRAGMA query_only = ON")
    return conn


def resolve_chat_jids(chat: str, whatsapp_db: str) -> list[str]:
    """Expand a phone number / JID to every chat JID that is the same conversation (phone + LID)."""
    if chat.endswith("@g.us"):
        return [chat]

    user, _, server = chat.partition("@")
    user = user.split(":")[0]
    if not server:
        user = re.sub(r"\D", "", user)
        server = "s.whatsapp.net"
    if not user:
        sys.exit(f"cannot parse chat: {chat}")

    jids = {f"{user}@{server}"}
    conn = ro_connect(whatsapp_db)
    try:
        if server == "lid":
            rows = conn.execute("SELECT pn FROM whatsmeow_lid_map WHERE lid = ?", (user,)).fetchall()
            jids.update(f"{r[0]}@s.whatsapp.net" for r in rows)
        else:
            rows = conn.execute("SELECT lid FROM whatsmeow_lid_map WHERE pn = ?", (user,)).fetchall()
            jids.update(f"{r[0]}@lid" for r in rows)
    finally:
        conn.close()
    return sorted(jids)


def load_audio_rows(messages_db: str, jids: list[str]) -> list[dict]:
    conn = ro_connect(messages_db)
    try:
        marks = ",".join("?" * len(jids))
        cur = conn.execute(
            f"""SELECT id, chat_jid, sender, timestamp, is_from_me, url,
                       media_key, file_sha256, file_enc_sha256, file_length
                FROM messages
                WHERE media_type = 'audio' AND chat_jid IN ({marks})
                ORDER BY timestamp, id""",
            jids,
        )
        cols = [d[0] for d in cur.description]
        return [dict(zip(cols, r)) for r in cur.fetchall()]
    finally:
        conn.close()


def url_expiry(url: str) -> int | None:
    m = re.search(r"[?&]oe=([0-9A-Fa-f]+)", url or "")
    return int(m.group(1), 16) if m else None


def safe_component(s: str) -> str:
    return re.sub(r"[^A-Za-z0-9_-]", "_", s)


def output_name(row: dict) -> str:
    ts = re.sub(r"[^0-9]", "", row["timestamp"][:19])  # YYYYMMDDHHMMSS
    stamp = f"{ts[:4]}-{ts[4:6]}-{ts[6:8]}_{ts[8:14]}"
    who = "me" if row["is_from_me"] else "them"
    return f"{stamp}_{who}_{safe_component(row['id'])}.ogg"


def decrypt_media(enc: bytes, media_key: bytes, file_enc_sha256: bytes, file_sha256: bytes) -> bytes:
    if hashlib.sha256(enc).digest() != file_enc_sha256:
        raise ValueError("encrypted file hash mismatch")
    expanded = HKDF(algorithm=hashes.SHA256(), length=112, salt=None, info=AUDIO_HKDF_INFO).derive(media_key)
    iv, cipher_key, mac_key = expanded[:16], expanded[16:48], expanded[48:80]
    ciphertext, mac = enc[:-10], enc[-10:]
    if not hmac.compare_digest(hmac.new(mac_key, iv + ciphertext, hashlib.sha256).digest()[:10], mac):
        raise ValueError("MAC mismatch")
    decryptor = Cipher(algorithms.AES(cipher_key), modes.CBC(iv)).decryptor()
    padded = decryptor.update(ciphertext) + decryptor.finalize()
    unpadder = padding.PKCS7(128).unpadder()
    plain = unpadder.update(padded) + unpadder.finalize()
    if hashlib.sha256(plain).digest() != file_sha256:
        raise ValueError("decrypted file hash mismatch")
    return plain


def fetch(url: str, timeout: int = 60) -> bytes:
    # Same Origin/Referer whatsmeow sends for media downloads.
    req = urllib.request.Request(url, headers={
        "User-Agent": USER_AGENT,
        "Origin": "https://web.whatsapp.com",
        "Referer": "https://web.whatsapp.com/",
    })
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read()


def write_atomic(path: str, data: bytes) -> None:
    tmp = path + ".part"
    with open(tmp, "wb") as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("chat", help="phone number, @s.whatsapp.net / @lid JID, or @g.us group JID")
    ap.add_argument("--store", default="store", help="store directory (default: store)")
    ap.add_argument("--out", help="output dir (default: <store>/media/<chat key>)")
    ap.add_argument("--limit", type=int, default=0, help="stop after N download attempts (0 = all)")
    ap.add_argument("--skip-by-expiry", action="store_true",
                    help="skip URLs whose oe= expiry has passed without trying them (faster, may miss files)")
    ap.add_argument("--pace", type=float, default=0.25, help="seconds between downloads (default 0.25)")
    ap.add_argument("--dry-run", action="store_true", help="list what would be downloaded, fetch nothing")
    args = ap.parse_args()

    messages_db = os.path.join(args.store, "messages.db")
    whatsapp_db = os.path.join(args.store, "whatsapp.db")

    jids = resolve_chat_jids(args.chat, whatsapp_db)
    rows = load_audio_rows(messages_db, jids)  # DB connections are closed after this line

    key = args.chat if args.chat.endswith("@g.us") else re.sub(r"\D", "", args.chat.split("@")[0])
    out_dir = args.out or os.path.join(args.store, "media", safe_component(key))
    print(f"chat JIDs: {', '.join(jids)}")
    print(f"audio rows: {len(rows)}  ->  {out_dir}")

    if not args.dry_run:
        os.makedirs(out_dir, exist_ok=True)

    index_path = os.path.join(out_dir, "index.json")
    index = {}
    if os.path.isfile(index_path):
        with open(index_path) as f:
            index = {e["message_id"]: e for e in json.load(f)}

    now = time.time()
    counts = {"ok": 0, "cached": 0, "expired": 0, "failed": 0, "incomplete": 0}
    attempts = 0

    # Newest first: fresh CDN links are the ones most likely to still work.
    for row in reversed(rows):
        name = output_name(row)
        path = os.path.join(out_dir, name)
        entry = {
            "message_id": row["id"], "chat_jid": row["chat_jid"], "timestamp": row["timestamp"],
            "from_me": bool(row["is_from_me"]), "file": name,
        }

        if os.path.isfile(path) and os.path.getsize(path) > 0:
            counts["cached"] += 1
            entry["status"] = "ok"
            index[row["id"]] = {**index.get(row["id"], {}), **entry}
            continue

        if not (row["url"] and row["media_key"] and len(row["media_key"]) == 32
                and row["file_sha256"] and row["file_enc_sha256"]):
            counts["incomplete"] += 1
            entry["status"] = "incomplete_metadata"
            index[row["id"]] = entry
            continue

        oe = url_expiry(row["url"])
        if oe is not None and oe < now and args.skip_by_expiry:
            counts["expired"] += 1
            entry["status"] = "expired_link"
            entry["link_expired_at"] = time.strftime("%Y-%m-%d", time.gmtime(oe))
            index[row["id"]] = entry
            continue

        if args.limit and attempts >= args.limit:
            break
        attempts += 1

        if args.dry_run:
            print(f"  would download {name}")
            continue

        try:
            enc = fetch(row["url"])
            plain = decrypt_media(enc, row["media_key"], row["file_enc_sha256"], row["file_sha256"])
            write_atomic(path, plain)
            counts["ok"] += 1
            entry.update(status="ok", bytes=len(plain), sha256=hashlib.sha256(plain).hexdigest())
            print(f"  {'ok':13s} {name} ({len(plain)} bytes)")
        except urllib.error.HTTPError as e:
            status = "expired_link" if e.code in (403, 404, 410) else f"http_{e.code}"
            counts["expired" if status == "expired_link" else "failed"] += 1
            entry.update(status=status, http_status=e.code)
            print(f"  {status:13s} {name}")
        except Exception as e:  # network error, hash/MAC mismatch
            counts["failed"] += 1
            entry.update(status="failed", error=str(e))
            print(f"  {'failed':13s} {name}: {e}")
        index[row["id"]] = entry
        time.sleep(args.pace)

    if not args.dry_run:
        write_atomic(index_path, json.dumps(sorted(index.values(), key=lambda e: (e["timestamp"], e["message_id"])),
                                            indent=2, ensure_ascii=False).encode())

    print("summary: " + ", ".join(f"{k}={v}" for k, v in counts.items()))
    return 0 if counts["failed"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
