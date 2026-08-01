package pdf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDocStructure(t *testing.T) {
	d := New("Test report")
	d.Title("Test report")
	d.Heading("Section (one)")
	d.Line("A body line with special chars: (parens) \\ backslash — em dash • bullet.")
	for i := 0; i < 120; i++ { // force multiple pages
		d.Line(fmt.Sprintf("Filler line %d with enough words to be plausible content in a report.", i))
	}
	out := d.Bytes()

	if !bytes.HasPrefix(out, []byte("%PDF-1.4")) {
		t.Fatal("missing PDF header")
	}
	if !bytes.HasSuffix(bytes.TrimSpace(out), []byte("%%EOF")) {
		t.Fatal("missing EOF marker")
	}
	if n := bytes.Count(out, []byte("/Type /Page ")); n < 2 {
		t.Fatalf("expected multiple pages, got %d", n)
	}
	// Every xref offset must point at the right object header.
	body := string(out)
	xref := body[strings.Index(body, "xref\n"):]
	lines := strings.Split(xref, "\n")[3:] // skip "xref", "0 N", and the free entry
	objNum := 0
	for _, l := range lines {
		if !strings.HasSuffix(l, " n ") {
			break
		}
		objNum++
		var off int
		if _, err := fmt.Sscanf(l, "%010d", &off); err != nil {
			t.Fatalf("parse xref offset %q: %v", l, err)
		}
		want := fmt.Sprintf("%d 0 obj", objNum)
		if !strings.HasPrefix(body[off:], want) {
			t.Fatalf("xref entry %d points at %q, want %q", objNum, body[off:off+12], want)
		}
	}
	if objNum < 6 {
		t.Fatalf("suspiciously few xref entries: %d", objNum)
	}
	// Parens must be escaped inside the content stream.
	if strings.Contains(body, "(Section (one)") {
		t.Error("unescaped parenthesis in content stream")
	}
}

// When pdftotext is available (locally), prove a real reader extracts our text.
func TestDocExtractableText(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	d := New("Extraction check")
	d.Title("Extraction check")
	d.Heading("Roster")
	d.Line("Ada Lovelace - treasurer - ada@example.org")
	f, err := os.CreateTemp(t.TempDir(), "*.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(d.Bytes()); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command("pdftotext", f.Name(), "-").Output()
	if err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	for _, want := range []string{"Extraction check", "Roster", "Ada Lovelace", "ada@example.org"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("extracted text missing %q:\n%s", want, out)
		}
	}
}

// verifyIntegrity mirrors ops/verify-pdf-export.py: zero the digest, hash, compare.
func verifyIntegrity(t *testing.T, data []byte) (string, bool) {
	t.Helper()
	marker := []byte(esc(integrityMarker))
	i := bytes.Index(data, marker)
	if i < 0 {
		t.Fatal("integrity marker not found")
	}
	start := i + len(marker)
	printed := string(data[start : start+64])
	zeroed := append([]byte(nil), data...)
	copy(zeroed[start:], integrityPlaceholder)
	sum := sha256.Sum256(zeroed)
	return printed, hex.EncodeToString(sum[:]) == printed
}

func TestFinalize_IntegrityRoundTripAndTamper(t *testing.T) {
	d := New("Sealed report")
	d.Watermark("Exported by ada@example.org - 2026-07-31 13:00 UTC")
	d.Title("Sealed report")
	d.Line("Some content that must not change.")
	out, digest := d.Finalize()

	printed, ok := verifyIntegrity(t, out)
	if !ok {
		t.Fatal("fresh document must verify")
	}
	if printed != digest {
		t.Fatalf("printed digest %s != returned %s", printed, digest)
	}

	// Any byte flip in the content breaks verification.
	tampered := append([]byte(nil), out...)
	idx := bytes.Index(tampered, []byte("must not change"))
	if idx < 0 {
		t.Fatal("content text not found")
	}
	tampered[idx] ^= 0x01
	if _, ok := verifyIntegrity(t, tampered); ok {
		t.Fatal("tampered document must NOT verify")
	}
	// Editing the printed hash itself also fails (it no longer matches).
	tampered2 := append([]byte(nil), out...)
	j := bytes.Index(tampered2, []byte(esc(integrityMarker))) + len(esc(integrityMarker))
	tampered2[j] = 'f'
	if _, ok := verifyIntegrity(t, tampered2); ok && printed[0] != 'f' {
		t.Fatal("forged digest must NOT verify")
	}
}

func TestWatermark_RenderedUnderContentAndInFooter(t *testing.T) {
	d := New("WM")
	d.Watermark("Exported by ada@example.org - 2026-07-31 13:00 UTC")
	d.Line("body")
	out := string(d.Bytes())
	if !strings.Contains(out, "0.88 g") {
		t.Error("light-gray watermark fill missing")
	}
	if !strings.Contains(out, "Tm (Exported by ada@example.org") {
		t.Error("diagonal watermark text missing")
	}
	// The watermark op must precede the body text in the content stream.
	if strings.Index(out, "0.88 g") > strings.Index(out, "(body)") {
		t.Error("watermark must render beneath the content")
	}
	if !strings.Contains(out, "Exported by ada@example.org - 2026-07-31 13:00 UTC - WM - page 1 of 1") {
		t.Error("footer must carry the exporter stamp")
	}
	// Extraction still works with pdftotext when available (visual layer sanity).
	if _, err := exec.LookPath("pdftotext"); err == nil {
		f, _ := os.CreateTemp(t.TempDir(), "*.pdf")
		d2 := New("WM2")
		d2.Watermark("Exported by ada@example.org")
		d2.Line("visible body line")
		b, _ := d2.Finalize()
		if _, err := f.Write(b); err != nil {
			t.Fatalf("write temp pdf: %v", err)
		}
		f.Close()
		txt, err := exec.Command("pdftotext", f.Name(), "-").Output()
		if err != nil {
			t.Fatalf("pdftotext: %v", err)
		}
		if !strings.Contains(string(txt), "visible body line") {
			t.Error("body text lost after watermark/finalize")
		}
		if !strings.Contains(string(txt), "Integrity (SHA-256):") {
			t.Error("integrity line missing from rendered text")
		}
	}
}
