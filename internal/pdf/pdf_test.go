package pdf

import (
	"bytes"
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
		fmt.Sscanf(l, "%010d", &off)
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
