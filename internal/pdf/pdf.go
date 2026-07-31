// Package pdf implements a small, dependency-free PDF writer for text reports:
// US-Letter pages, Helvetica/Helvetica-Bold, word wrapping, automatic page
// breaks, and page-number footers. It deliberately supports exactly what the
// report endpoints need — headings and wrapped lines — keeping the project's
// no-third-party-dependency footprint (compare internal/metrics, the ICS
// writer). Text is emitted in WinAnsi (Latin-1); common typographic characters
// are transliterated and anything else becomes '?'.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
)

const (
	pageW      = 612.0 // US Letter, points
	pageH      = 792.0
	marginX    = 54.0
	marginTop  = 60.0
	marginBot  = 56.0
	bodySize   = 10.0
	bodyLead   = 14.0
	headSize   = 13.0
	headLead   = 20.0
	titleSize  = 17.0
	titleLead  = 26.0
	footerSize = 8.0
	// Average Helvetica glyph width ≈ 0.50 em; be slightly conservative so
	// wrapped lines never overflow the right margin.
	avgCharEm = 0.52
)

// Doc accumulates styled lines and renders a complete PDF.
type Doc struct {
	title string
	pages []*bytes.Buffer
	y     float64
}

// New starts a document; title is used for the metadata and the first heading
// is up to the caller.
func New(title string) *Doc {
	d := &Doc{title: title}
	d.addPage()
	return d
}

func (d *Doc) addPage() {
	d.pages = append(d.pages, &bytes.Buffer{})
	d.y = pageH - marginTop
}

func (d *Doc) ensure(lead float64) {
	if d.y-lead < marginBot {
		d.addPage()
	}
}

// maxChars returns how many characters fit one line at the given font size.
func maxChars(size float64) int {
	return int((pageW - 2*marginX) / (size * avgCharEm))
}

// esc transliterates to Latin-1 and escapes PDF string syntax.
func esc(s string) string {
	repl := strings.NewReplacer(
		"—", "-", "–", "-", "’", "'", "‘", "'", "“", `"`, "”", `"`,
		"…", "...", "•", "*", "\t", "  ", "·", "-", "→", "->", "⛓", "#",
	)
	s = repl.Replace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '(':
			b.WriteString(`\(`)
		case r == ')':
			b.WriteString(`\)`)
		case r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r < 32:
			// skip control characters
		case r <= 255:
			b.WriteByte(byte(r))
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// wrap splits text into lines of at most width characters, breaking on words
// (a single overlong word is hard-split rather than overflowing).
func wrap(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, w := range words {
			for len(w) > width { // hard-split pathological words
				if line != "" {
					out = append(out, line)
					line = ""
				}
				out = append(out, w[:width])
				w = w[width:]
			}
			switch {
			case line == "":
				line = w
			case len(line)+1+len(w) <= width:
				line += " " + w
			default:
				out = append(out, line)
				line = w
			}
		}
		out = append(out, line)
	}
	return out
}

func (d *Doc) text(font string, size, lead, indent float64, line string) {
	d.ensure(lead)
	d.y -= lead
	page := d.pages[len(d.pages)-1]
	fmt.Fprintf(page, "BT /%s %.1f Tf %.1f %.1f Td (%s) Tj ET\n",
		font, size, marginX+indent, d.y, esc(line))
}

// Title renders the document title (large, bold).
func (d *Doc) Title(s string) {
	for _, l := range wrap(s, maxChars(titleSize)) {
		d.text("F2", titleSize, titleLead, 0, l)
	}
}

// Heading renders a bold section heading with breathing room above.
func (d *Doc) Heading(s string) {
	d.ensure(headLead + 6)
	d.y -= 6
	for _, l := range wrap(s, maxChars(headSize)) {
		d.text("F2", headSize, headLead, 0, l)
	}
}

// Line renders one wrapped body line.
func (d *Doc) Line(s string) {
	for _, l := range wrap(s, maxChars(bodySize)) {
		d.text("F1", bodySize, bodyLead, 0, l)
	}
}

// Indented renders a wrapped body line at an indent (for sub-items).
func (d *Doc) Indented(s string) {
	for _, l := range wrap(s, maxChars(bodySize)-4) {
		d.text("F1", bodySize, bodyLead, 18, l)
	}
}

// Bold renders one wrapped bold body line.
func (d *Doc) Bold(s string) {
	for _, l := range wrap(s, maxChars(bodySize)) {
		d.text("F2", bodySize, bodyLead, 0, l)
	}
}

// Space adds vertical whitespace.
func (d *Doc) Space() { d.y -= bodyLead / 2 }

// Bytes assembles the final PDF.
func (d *Doc) Bytes() []byte {
	// Footers (page x of y) are stamped now that the page count is known.
	total := len(d.pages)
	for i, p := range d.pages {
		fmt.Fprintf(p, "BT /F1 %.1f Tf %.1f %.1f Td (%s) Tj ET\n",
			footerSize, marginX, marginBot-24,
			esc(fmt.Sprintf("%s - page %d of %d", d.title, i+1, total)))
	}

	var buf bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}

	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	// 1: catalog, 2: pages tree, 3: F1, 4: F2, then per page: page obj + stream.
	kids := make([]string, total)
	for i := range d.pages {
		kids[i] = fmt.Sprintf("%d 0 R", 5+i*2)
	}
	obj(`<< /Type /Catalog /Pages 2 0 R >>`)
	obj(fmt.Sprintf(`<< /Type /Pages /Kids [%s] /Count %d >>`, strings.Join(kids, " "), total))
	obj(`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>`)
	obj(`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>`)
	for i, p := range d.pages {
		streamRef := 6 + i*2
		obj(fmt.Sprintf(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>`,
			pageW, pageH, streamRef))
		content := p.Bytes()
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}

	xrefAt := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, xrefAt)
	return buf.Bytes()
}
