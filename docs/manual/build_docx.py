#!/usr/bin/env python3
"""Convert the manual Markdown (with img/ screenshots) into a .docx suitable for
Feishu import: headings, bold/code inline styles, bullet/ordered lists, tables,
blockquotes, images and horizontal rules.

Usage: python3 build_docx.py 使用手册.md 使用手册.docx "标题"
"""
import re
import sys
from pathlib import Path

from docx import Document
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.shared import Inches, Pt, RGBColor


def add_runs(par, text, base_bold=False):
    """Parse **bold**, `code`, [label](url) and *italic* into runs."""
    token = re.compile(r"(\*\*.+?\*\*|`[^`]+`|\[[^\]]+\]\([^)]+\)|\*[^*]+\*)")
    for piece in token.split(text):
        if not piece:
            continue
        if piece.startswith("**") and piece.endswith("**"):
            r = par.add_run(piece[2:-2]); r.bold = True
        elif piece.startswith("`") and piece.endswith("`"):
            r = par.add_run(piece[1:-1]); r.font.name = "Consolas"
            r.font.size = Pt(9.5)
        elif piece.startswith("[") and "](" in piece:
            label, url = re.match(r"\[([^\]]+)\]\(([^)]+)\)", piece).groups()
            r = par.add_run(label); r.font.color.rgb = RGBColor(0x33, 0x70, 0xFF)
            r.underline = True
            if not url.startswith("img/"):
                par.add_run(f" ({url})").font.size = Pt(9)
        elif piece.startswith("*") and piece.endswith("*") and len(piece) > 2:
            r = par.add_run(piece[1:-1]); r.italic = True
        else:
            r = par.add_run(piece)
        if base_bold:
            r.bold = True


def convert(md_path: Path, out_path: Path, title: str):
    doc = Document()
    style = doc.styles["Normal"]
    style.font.name = "Calibri"
    style.font.size = Pt(10.5)

    lines = md_path.read_text(encoding="utf-8").splitlines()
    i = 0
    first_h1_done = False
    while i < len(lines):
        line = lines[i].rstrip()
        stripped = line.strip()

        if stripped.startswith("<!--"):  # 导出时忽略的 HTML 注释
            i += 1
            continue

        # fenced code block
        if stripped.startswith("```"):
            i += 1
            buf = []
            while i < len(lines) and not lines[i].strip().startswith("```"):
                buf.append(lines[i])
                i += 1
            i += 1  # closing fence
            p = doc.add_paragraph()
            p.paragraph_format.left_indent = Inches(0.2)
            r = p.add_run("\n".join(buf))
            r.font.name = "Consolas"
            r.font.size = Pt(9)
            continue

        # table block
        if stripped.startswith("|") and i + 1 < len(lines) and set(lines[i + 1].strip()) <= set("|-: "):
            header = [c.strip() for c in stripped.strip("|").split("|")]
            rows = []
            i += 2
            while i < len(lines) and lines[i].strip().startswith("|"):
                rows.append([c.strip() for c in lines[i].strip().strip("|").split("|")])
                i += 1
            table = doc.add_table(rows=1, cols=len(header))
            table.style = "Light Grid Accent 1"
            for j, h in enumerate(header):
                cell = table.rows[0].cells[j]
                cell.text = ""
                add_runs(cell.paragraphs[0], h, base_bold=True)
            for row in rows:
                cells = table.add_row().cells
                for j, c in enumerate(row[: len(header)]):
                    cells[j].text = ""
                    add_runs(cells[j].paragraphs[0], c)
            doc.add_paragraph()
            continue

        # image
        m = re.match(r"!\[([^\]]*)\]\(([^)]+)\)", stripped)
        if m:
            img = md_path.parent / m.group(2)
            if img.exists():
                doc.add_picture(str(img), width=Inches(6.2))
                doc.paragraphs[-1].alignment = WD_ALIGN_PARAGRAPH.CENTER
                cap = doc.add_paragraph()
                cap.alignment = WD_ALIGN_PARAGRAPH.CENTER
                r = cap.add_run(m.group(1))
                r.italic = True; r.font.size = Pt(9)
                r.font.color.rgb = RGBColor(0x64, 0x6A, 0x73)
            i += 1
            continue

        if stripped in ("---", "***"):
            doc.add_paragraph().add_run("—" * 30).font.color.rgb = RGBColor(0xCC, 0xCC, 0xCC)
            i += 1
            continue

        hm = re.match(r"^(#{1,6})\s+(.*)$", stripped)
        if hm:
            level, text = len(hm.group(1)), hm.group(2)
            if level == 1 and not first_h1_done:
                first_h1_done = True
                h = doc.add_heading(title, level=0)
            else:
                h = doc.add_heading("", level=min(level, 4))
                add_runs(h, text)
            i += 1
            continue

        bm = re.match(r"^[-*]\s+(.*)$", stripped)
        if bm:
            p = doc.add_paragraph(style="List Bullet")
            add_runs(p, bm.group(1))
            i += 1
            continue

        om = re.match(r"^(\d+)\.\s+(.*)$", stripped)
        if om:
            p = doc.add_paragraph(style="List Number")
            add_runs(p, om.group(2))
            i += 1
            continue

        if stripped.startswith(">"):
            p = doc.add_paragraph()
            p.paragraph_format.left_indent = Inches(0.3)
            add_runs(p, stripped.lstrip("> ").strip())
            for r in p.runs:
                r.font.color.rgb = RGBColor(0x41, 0x46, 0x4F)
            i += 1
            continue

        if not stripped:
            i += 1
            continue

        p = doc.add_paragraph()
        add_runs(p, stripped)
        i += 1

    doc.save(out_path)
    return out_path


if __name__ == "__main__":
    src, dst, title = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3]
    convert(src, dst, title)
    print("wrote", dst, dst.stat().st_size, "bytes")
