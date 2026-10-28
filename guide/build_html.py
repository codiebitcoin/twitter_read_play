#!/usr/bin/env python3
"""Render a usage-guide Markdown file into a self-contained HTML file.

Images referenced relatively (img/... or img/en/...) are embedded as base64
data URIs so the HTML prints cleanly to PDF on its own.

Usage: python3 build_html.py USAGE.md USAGE.html "Codie Media Broadcast User Guide"
"""
import base64
import mimetypes
import re
import sys
from pathlib import Path

import markdown

STYLE = """
body { font-family: "Noto Sans CJK SC","PingFang SC","Microsoft YaHei",-apple-system,sans-serif; margin:0; color:#1f2329; }
.wrap { max-width: 900px; margin: 0 auto; padding: 40px 48px; }
h1 { font-size: 28px; border-bottom: 3px solid #3370ff; padding-bottom: 10px; }
h2 { font-size: 21px; margin-top: 36px; border-left: 5px solid #3370ff; padding-left: 10px; }
h3 { font-size: 17px; margin-top: 24px; }
h4 { font-size: 15px; margin-top: 18px; }
img { max-width: 100%; border: 1px solid #dee0e3; border-radius: 8px; margin: 10px 0; box-shadow: 0 2px 8px rgba(0,0,0,.08); }
table { border-collapse: collapse; width: 100%; margin: 12px 0; font-size: 14px; }
th,td { border: 1px solid #dee0e3; padding: 8px 10px; text-align: left; }
th { background: #f5f6f7; }
blockquote { background:#f5f8ff; border-left:4px solid #3370ff; margin:12px 0; padding:8px 14px; color:#41464f; border-radius:0 8px 8px 0; }
code { background:#f2f3f5; padding:2px 5px; border-radius:4px; font-size:13px; }
pre { background:#f5f6f7; border:1px solid #dee0e3; border-radius:8px; padding:12px 14px; overflow:auto; white-space:pre-wrap; }
pre code { background:none; padding:0; font-size:13px; }
a { color:#3370ff; }
li { margin: 4px 0; }
hr { border:none; border-top:1px solid #dee0e3; margin:28px 0; }
h1,h2,h3,h4 { break-after: avoid; }
img { break-inside: avoid; }
tr { break-inside: avoid; }
@media print { .wrap { padding: 12mm 10mm; max-width: none; } }
"""

IMG_RE = re.compile(r'<img([^>]*?)src="([^"]+)"')


def embed(md_path: Path, html: str) -> str:
    def repl(m):
        src = m.group(2)
        if src.startswith(("http://", "https://", "data:")):
            return m.group(0)
        path = md_path.parent / src
        if not path.exists():
            return m.group(0)
        mime = mimetypes.guess_type(path.name)[0] or "image/png"
        data = base64.b64encode(path.read_bytes()).decode("ascii")
        return f'<img{m.group(1)}src="data:{mime};base64,{data}"'

    return IMG_RE.sub(repl, html)


def main():
    src, dst, title = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3]
    text = src.read_text(encoding="utf-8")
    body = markdown.markdown(
        text,
        extensions=["tables", "fenced_code", "sane_lists", "toc"],
        output_format="html5",
    )
    body = embed(src, body)
    html = (
        '<!DOCTYPE html><html><head><meta charset="utf-8">\n'
        f"<title>{title}</title>\n<style>{STYLE}</style>\n"
        f'</head><body><div class="wrap">{body}</div></body></html>\n'
    )
    dst.write_text(html, encoding="utf-8")
    print("wrote", dst, dst.stat().st_size, "bytes")


if __name__ == "__main__":
    main()
