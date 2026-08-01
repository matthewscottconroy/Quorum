#!/usr/bin/env python3
"""Independent integrity verifier for a Quorum PDF report.

Every Quorum PDF report carries a line "Integrity (SHA-256): <64 hex digits>"
computed over the exact bytes of the document with those 64 digits set to
zeros. To verify, this script zeros them again, hashes the file, and compares
— Python standard library only, no Quorum software, no network.

An intact hash proves the FILE was not altered since export. To prove it is a
GENUINE Quorum export (and not a fabricated file with a self-consistent hash),
match the digest against the audit log's EXPORT entry for this document — the
audit log is hash-chained and independently verifiable (COMPLIANCE.md,
verify-audit-export.py).

Usage:  python3 verify-pdf-export.py report.pdf
Exit 0: intact (digest printed — match it against the audit log).
Exit 1: tampered or not a stamped Quorum report.
"""
import hashlib, re, sys

MARKER = rb"Integrity \\\(SHA-256\\\): "  # parens are backslash-escaped inside PDF strings

def main(path):
    data = bytearray(open(path, "rb").read())
    m = re.search(MARKER + rb"([0-9a-f]{64})", data)
    if not m:
        sys.exit("no integrity stamp found — not a stamped Quorum report (or the stamp was removed)")
    printed = m.group(1).decode()
    start = m.start(1)
    data[start:start + 64] = b"0" * 64
    actual = hashlib.sha256(bytes(data)).hexdigest()
    if actual != printed:
        print(f"printed digest:  {printed}")
        print(f"recomputed:      {actual}")
        sys.exit("TAMPERED: the document does not match its integrity stamp")
    print(f"document INTACT - SHA-256 {printed}")
    print("Now confirm authenticity: this digest must appear in the audit log's")
    print("matching 'EXPORT ...' entry (detail.sha256), whose chain verifies.")

if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    main(sys.argv[1])
