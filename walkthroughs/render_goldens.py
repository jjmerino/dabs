#!/usr/bin/env python3
"""Render the tuti goldens (ANSI terminal captures) into one self-contained HTML
gallery, so the screens the walkthroughs assert can be eyeballed in colour
rather than read as escape codes.

    python3 walkthroughs/render_goldens.py > screendiffs.html   # or pass a path

Stdlib only; reads `walkthroughs/goldens/**`, writes a single HTML file.
"""
from __future__ import annotations

import html
import re
import sys
from pathlib import Path

GOLDENS = Path(__file__).parent / "goldens"

# xterm-256 palette → hex.
BASE16 = [
    "000000", "800000", "008000", "808000", "000080", "800080", "008080", "c0c0c0",
    "808080", "ff0000", "00ff00", "ffff00", "0000ff", "ff00ff", "00ffff", "ffffff",
]


def _cube(v: int) -> int:  # 0..5 → 0,95,135,175,215,255
    return 0 if v == 0 else 55 + 40 * v


def xterm(n: int) -> str:
    if n < 16:
        return "#" + BASE16[n]
    if n < 232:
        n -= 16
        r, g, b = _cube(n // 36), _cube((n // 6) % 6), _cube(n % 6)
        return f"#{r:02x}{g:02x}{b:02x}"
    v = 8 + 10 * (n - 232)
    return f"#{v:02x}{v:02x}{v:02x}"


SGR = re.compile(r"\x1b\[([0-9;]*)m")


def ansi_to_html(text: str) -> str:
    """ANSI-SGR string → HTML spans. Handles the codes the goldens use: 256-colour
    fg/bg (38;5;N / 48;5;N), default fg/bg (39/49), bold (1), normal (22), reset (0)."""
    out, fg, bg, bold, pos = [], None, None, False, 0

    def span() -> str:
        s = []
        if fg:
            s.append(f"color:{fg}")
        if bg:
            s.append(f"background:{bg}")
        if bold:
            s.append("font-weight:700")
        return f'<span style="{";".join(s)}">' if s else "<span>"

    for m in SGR.finditer(text):
        chunk = text[pos:m.start()]
        if chunk:
            out.append(span() + html.escape(chunk) + "</span>")
        pos = m.end()
        codes = m.group(1).split(";") or ["0"]
        i = 0
        while i < len(codes):
            c = codes[i] or "0"
            if c == "0":
                fg = bg = None
                bold = False
            elif c == "1":
                bold = True
            elif c == "22":
                bold = False
            elif c == "39":
                fg = None
            elif c == "49":
                bg = None
            elif c == "38" and i + 2 < len(codes) and codes[i + 1] == "5":
                fg = xterm(int(codes[i + 2]))
                i += 2
            elif c == "48" and i + 2 < len(codes) and codes[i + 1] == "5":
                bg = xterm(int(codes[i + 2]))
                i += 2
            i += 1
    chunk = text[pos:]
    if chunk:
        out.append(span() + html.escape(chunk) + "</span>")
    return "".join(out)


# Guide pages in reading order; dir → (title, is_new_journey).
PAGES = [
    ("quickstart", "Quickstart", False),
    ("recipes", "Recipes", False),
    ("sources", "Sources", False),
    ("spaces", "Spaces", False),
    ("exec", "Exec", False),
    ("names", "Named nodes", True),
    ("egress", "Egress", True),
    ("worktrees", "Worktrees", False),
    ("rm", "Listing & cleanup", False),
]

STYLE = """
<style>
  :root{--bg:#f4f4f6;--panel:#fff;--ink:#1a1a22;--muted:#5c5c6a;--line:#e3e3ea;
    --accent:#8E17FF;--accent-2:#B05CFF;--term-bg:#14131a;--term-ink:#e7e7ee;
    --sans:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;
    --mono:ui-monospace,"SF Mono",Menlo,"Cascadia Code","JetBrains Mono",Consolas,monospace;}
  @media (prefers-color-scheme:dark){:root{--bg:#0d0d12;--panel:#16161d;--ink:#ececf2;
    --muted:#9a9aa8;--line:#26262f;--accent:#B05CFF;--accent-2:#8E17FF;--term-bg:#0a0a0f;}}
  :root[data-theme="light"]{--bg:#f4f4f6;--panel:#fff;--ink:#1a1a22;--muted:#5c5c6a;
    --line:#e3e3ea;--accent:#8E17FF;--accent-2:#B05CFF;--term-bg:#14131a;--term-ink:#e7e7ee;}
  :root[data-theme="dark"]{--bg:#0d0d12;--panel:#16161d;--ink:#ececf2;--muted:#9a9aa8;
    --line:#26262f;--accent:#B05CFF;--accent-2:#8E17FF;--term-bg:#0a0a0f;--term-ink:#e7e7ee;}
  *{box-sizing:border-box}
  body{margin:0;background:var(--bg);color:var(--ink);font-family:var(--sans);
    line-height:1.5;-webkit-font-smoothing:antialiased}
  header,main,footer{max-width:1180px;margin-inline:auto;padding-inline:24px}
  header{padding-top:56px;padding-bottom:24px;border-bottom:1px solid var(--line)}
  .eyebrow{font-size:12px;letter-spacing:.14em;text-transform:uppercase;
    color:var(--accent);font-weight:600;margin:0 0 8px}
  h1{font-size:clamp(30px,4vw,44px);margin:0 0 12px;letter-spacing:-.02em;text-wrap:balance}
  .lede{max-width:65ch;color:var(--muted);font-size:16px;margin:0 0 20px}
  .lede strong{color:var(--ink);font-weight:600}
  .mark{color:var(--accent);font-weight:600;white-space:nowrap}
  nav{display:flex;flex-wrap:wrap;gap:6px 14px;font-size:14px}
  nav a{color:var(--muted);text-decoration:none;border-bottom:1px solid transparent}
  nav a:hover{color:var(--accent);border-color:var(--accent)}
  main{padding-top:8px}
  section{padding:36px 0 8px;border-bottom:1px solid var(--line)}
  h2{display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;font-size:22px;
    letter-spacing:-.01em;margin:0 0 20px}
  h2 .path{font-family:var(--mono);font-size:12px;color:var(--muted);font-weight:400}
  .badge{font-size:11px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;
    color:#fff;background:linear-gradient(90deg,var(--accent),var(--accent-2));
    padding:3px 9px;border-radius:999px}
  .grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(340px,1fr));gap:18px}
  .term{margin:0;border:1px solid var(--line);border-radius:10px;overflow:hidden;
    background:var(--term-bg);box-shadow:0 1px 2px rgba(0,0,0,.18)}
  figcaption{display:flex;align-items:center;gap:7px;padding:9px 12px;
    background:rgba(255,255,255,.04);border-bottom:1px solid rgba(255,255,255,.06)}
  .dot{width:10px;height:10px;border-radius:50%;background:#3a3a46;flex:none}
  .dot:first-child{background:#ff5f56}.dot:nth-child(2){background:#ffbd2e}
  .dot:nth-child(3){background:#27c93f}
  .name{margin-left:8px;font-family:var(--mono);font-size:12px;color:#b9b9c6}
  .term pre{margin:0;padding:14px 16px;overflow-x:auto;color:var(--term-ink);
    font-family:var(--mono);font-size:12.5px;line-height:1.45;white-space:pre;
    tab-size:8;font-variant-numeric:tabular-nums}
  footer{padding:28px 24px 60px;color:var(--muted);font-size:13px}
  footer code{font-family:var(--mono);font-size:12px}
  @media (max-width:520px){.grid{grid-template-columns:1fr}}
</style>
"""


def build() -> str:
    sections, nav_links, total, pages = [], [], 0, 0
    for page, title, is_new in PAGES:
        d = GOLDENS / page
        if not d.exists():
            continue
        pages += 1
        cards = []
        for f in sorted(d.glob("*.txt")):
            total += 1
            body = ansi_to_html(f.read_text())
            cards.append(
                f'<figure class="term"><figcaption><span class="dot"></span>'
                f'<span class="dot"></span><span class="dot"></span>'
                f'<span class="name">{html.escape(f.stem)}</span></figcaption>'
                f"<pre>{body}</pre></figure>"
            )
        badge = '<span class="badge">new journey</span>' if is_new else ""
        sections.append(
            f'<section id="{page}"><h2>{html.escape(title)}'
            f'<span class="path">walkthroughs/goldens/{page}/</span>{badge}</h2>'
            f'<div class="grid">{"".join(cards)}</div></section>'
        )
        nav_links.append(f'<a href="#{page}">{html.escape(title)}{" ✦" if is_new else ""}</a>')

    body = f"""<title>dabs — screendiff gallery</title>{STYLE}
<header>
  <p class="eyebrow">dabs · manual walkthroughs</p>
  <h1>Screendiff gallery</h1>
  <p class="lede">Every screen the tuti walkthroughs assert, rendered in colour from
  its golden — a verbatim capture of the <strong>released</strong> dabs CLI driven in
  a box, the same text the docs quote. {total} screens across {pages} guides;
  <span class="mark">✦ marks the two new journeys</span>.</p>
  <nav>{" · ".join(nav_links)}</nav>
</header>
<main>{"".join(sections)}</main>
<footer>Generated by <code>walkthroughs/render_goldens.py</code> from
<code>walkthroughs/goldens/**</code> · colours are the terminal's own xterm-256 palette.</footer>
"""
    return body


if __name__ == "__main__":
    out = build()
    if len(sys.argv) > 1:
        Path(sys.argv[1]).write_text(out)
    else:
        sys.stdout.write(out)
