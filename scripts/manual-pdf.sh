#!/usr/bin/env bash
# Render USER_MANUAL.md to quorum-manual.pdf with the best tool available:
#   1. pandoc + xelatex/pdflatex  — title page, clickable TOC with page numbers
#   2. pandoc + Chrome/Chromium   — styled HTML printed headlessly to PDF
# Run via `make manual-pdf`.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=quorum-manual.pdf

if ! command -v pandoc >/dev/null 2>&1; then
    echo "manual-pdf: pandoc is required." >&2
    echo "  Fedora:        sudo dnf install pandoc texlive-scheme-small" >&2
    echo "  Debian/Ubuntu: sudo apt install pandoc texlive-latex-recommended" >&2
    exit 1
fi

engine=""
for e in xelatex pdflatex; do
    command -v "$e" >/dev/null 2>&1 && { engine="$e"; break; }
done

if [ -n "$engine" ]; then
    echo "==> pandoc + $engine"
    # Default LaTeX fonts (Latin Modern) — present in every TeX install; custom
    # fonts via -V mainfont are a per-machine gamble not worth taking here.
    # LaTeX fonts carry no emoji, so swap the two the manual uses for words.
    tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
    sed 's/🔥/"heat map"/g; s/🔒/"lock"/g' USER_MANUAL.md > "$tmp/manual.md"
    pandoc "$tmp/manual.md" -o "$OUT" --pdf-engine="$engine" --toc --toc-depth=3 \
          -V colorlinks=true -V linkcolor=NavyBlue -V urlcolor=NavyBlue \
          -V geometry:margin=1in -V fontsize=11pt
else
    chrome=""
    for c in chromium chromium-browser google-chrome google-chrome-stable; do
        command -v "$c" >/dev/null 2>&1 && { chrome="$c"; break; }
    done
    if [ -z "$chrome" ]; then
        echo "manual-pdf: need a LaTeX engine or Chrome/Chromium alongside pandoc." >&2
        echo "  Fedora: sudo dnf install texlive-scheme-small   (preferred)" >&2
        exit 1
    fi
    echo "==> pandoc + $chrome (headless print)"
    tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
    # \newpage is a LaTeX-ism; give the HTML path real page breaks.
    sed 's/^\\newpage$/<div style="break-after: page;"><\/div>/' USER_MANUAL.md > "$tmp/manual.md"
    pandoc "$tmp/manual.md" -o "$tmp/manual.html" --standalone --toc --toc-depth=3 \
        --metadata title-suffix="" -V lang=en \
        -H <(cat <<'CSS'
<style>
  body { font: 11pt/1.55 Georgia, "DejaVu Serif", serif; color: #1a1a1a;
         max-width: 46rem; margin: 0 auto; }
  h1 { break-before: page; font-size: 1.7rem; border-bottom: 2px solid #244a80;
       padding-bottom: .2rem; }
  h1:first-of-type, header + * h1 { break-before: auto; }
  h2 { font-size: 1.25rem; color: #244a80; margin-top: 1.6rem; }
  h3 { font-size: 1.05rem; }
  code { font: .85em "DejaVu Sans Mono", monospace; background: #f3f4f6;
         padding: .08em .3em; border-radius: 3px; }
  pre { background: #f3f4f6; padding: .7em .9em; border-radius: 5px;
        overflow-x: auto; }
  pre code { background: none; padding: 0; }
  table { border-collapse: collapse; width: 100%; font-size: .92em; }
  th, td { border: 1px solid #c9ccd1; padding: .3em .55em; text-align: left; }
  th { background: #eef1f5; }
  blockquote { border-left: 4px solid #244a80; margin-left: 0;
               padding: .1em 1em; background: #f7f9fc; }
  #TOC { break-after: page; }
  a { color: #244a80; }
  @page { margin: 2cm 1.8cm; }
</style>
CSS
)
    "$chrome" --headless --disable-gpu --no-sandbox \
        --print-to-pdf="$PWD/$OUT" --no-pdf-header-footer \
        "file://$tmp/manual.html" 2>/dev/null
fi

ls -lh "$OUT"
echo "manual-pdf: wrote $OUT"
