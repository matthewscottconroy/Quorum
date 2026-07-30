#!/usr/bin/env python3
"""Independent verifier for a Quorum audit-log evidence export.

Give this file and the CSV (Audit log -> "Export evidence (CSV)") to your
accountant, lawyer, or auditor. It recomputes the export's SHA-256 hash chain
using only the Python standard library — no Quorum software, no network, no
trust in the operator. See COMPLIANCE.md for what an intact chain proves.

Usage:  python3 verify-audit-export.py quorum-audit-log.csv
Exit 0: chain intact (head hash printed — record it in your engagement notes).
Exit 1: tampering detected (first bad row printed).
"""
import csv, hashlib, sys

def main(path):
    rows = list(csv.reader(open(path, newline="")))
    if len(rows) < 2 or not rows[0] or rows[0][0] != "# chain_status":
        sys.exit("not a Quorum audit export (missing '# chain_status' stamp)")
    stamp, data = rows[0], rows[2:]
    prev = ""
    for r in data:
        seq, _id, uid, _email, action, etype, eid, ts, prev_hash, entry_hash = r
        if prev and prev_hash != prev:
            sys.exit(f"LINK BROKEN at seq {seq}: prev_hash does not match the "
                     f"previous row's entry_hash — rows were removed or reordered")
        payload = f"{seq}|{uid}|{action}|{etype}|{eid}|{ts}|{prev_hash}"
        if hashlib.sha256(payload.encode()).hexdigest() != entry_hash:
            sys.exit(f"HASH MISMATCH at seq {seq}: this row's content does not "
                     f"match its recorded hash — the row was altered")
        prev = entry_hash
    print(f"chain INTACT: {len(data)} entries")
    print(f"head hash (recomputed): {prev}")
    print(f"head hash (as exported): {stamp[7]}")
    if prev != stamp[7]:
        sys.exit("recomputed head differs from the export stamp — file truncated?")
    print("Record the head hash; compare it against the chain at your next engagement.")

if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    main(sys.argv[1])
