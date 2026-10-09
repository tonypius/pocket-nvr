#!/usr/bin/env python3
"""Scrub PII from run artifacts before publishing (bench logs, soak data,
supervisor logs). Stdlib only; runs on the Mac and on the phone.

Usage:
  scripts/scrub_for_publish.py [--in-place] [--extra STR]... FILE [FILE...]

Reads each file (or stdin when FILE is -), redacts, writes scrubbed content
to stdout (or back over the file with --in-place). Prints a redaction
count summary to stderr so a silent no-op is visible.

Redaction rules (NFR: published artifacts must survive a paranoid read):
  - home-directory paths: /Users/<name>, /home/<name> -> /Users/user
  - RFC1918 LAN IPs (not 127.0.0.1) -> last two octets zeroed (192.168.0.x)
  - MAC addresses -> redacted
  - token/secret/password/api-key assignments -> value redacted
    (covers "token=...", "Authorization: Bearer ...", ntfy topics)
  - ntfy.sh/<topic>, Telegram chat_id=<num> -> redacted
  - --extra: any literal string (adb serials, hostnames, SSIDs, ...)
"""
import argparse
import re
import sys

LAN_IP = re.compile(
    r"\b(10|127|192\.168|172\.(1[6-9]|2\d|3[01]))\.(\d{1,3})\.(\d{1,3})\b")
MAC = re.compile(r"\b(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}\b")
CRED = re.compile(
    r"(?i)\b(token|secret|secret_[a-z_]+|password|passwd|api[_-]?key|"
    r"authorization|bearer|chat_id)\b([=:\s]+)([A-Za-z0-9._+/=-]{6,})")
NTFY_TOPIC = re.compile(r"\bntfy\.sh/[A-Za-z0-9_-]+")
USER_PATH = re.compile(r"(/Users|/home)/[^/\s\"']+")


def scrub(text: str, extras: list) -> tuple[str, int]:
    hits = 0

    def count_sub(pat, repl, s):
        nonlocal hits
        s2, n = pat.subn(repl, s)
        hits += n
        return s2

    text = count_sub(USER_PATH, r"\1/user", text)
    text = count_sub(CRED, r"\1\2REDACTED", text)
    text = count_sub(NTFY_TOPIC, "ntfy.sh/TOPIC", text)
    text = count_sub(MAC, "REDACTED-MAC", text)

    def ip_zero(m):
        nonlocal hits
        if m.group(0) in ("127.0.0.1", "0.0.0.0"):
            return m.group(0)  # loopback/any are not identifying
        hits += 1
        return f"{m.group(1)}.0.x"

    text = LAN_IP.sub(ip_zero, text)

    for extra in extras:  # literals last: catch-all for serials, SSIDs, hosts
        if extra in text:
            hits += text.count(extra)
            text = text.replace(extra, "REDACTED")
    return text, hits


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("files", nargs="+", help="files to scrub, or - for stdin")
    ap.add_argument("--in-place", action="store_true")
    ap.add_argument("--extra", action="append", default=[],
                    help="literal string to redact (repeatable)")
    args = ap.parse_args()

    total = 0
    for path in args.files:
        raw = sys.stdin.read() if path == "-" else open(path).read()
        clean, hits = scrub(raw, args.extra)
        total += hits
        if args.in_place and path != "-":
            with open(path, "w") as f:
                f.write(clean)
        else:
            sys.stdout.write(clean)
    print(f"scrub: {total} redactions across {len(args.files)} file(s)",
          file=sys.stderr)
    sys.exit(0)


if __name__ == "__main__":
    main()
