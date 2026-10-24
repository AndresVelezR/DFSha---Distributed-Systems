#!/usr/bin/env python3
"""Exporta el informe Markdown a PDF. Dependencia de autoría: reportlab."""
from pathlib import Path
import html
import re

from reportlab.lib import colors
from reportlab.lib.enums import TA_CENTER, TA_JUSTIFY
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import ParagraphStyle, getSampleStyleSheet
from reportlab.lib.units import cm
from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer, PageBreak, Table, TableStyle, Preformatted, KeepTogether
from reportlab.graphics.shapes import Drawing, Rect, String, Line, Polygon

ROOT = Path(__file__).resolve().parents[1]
BLUE = colors.HexColor("#123B5D")
TEAL = colors.HexColor("#137C83")
WIDTH = A4[0] - 4 * cm
styles = getSampleStyleSheet()
styles.add(ParagraphStyle(name="BodyDF", fontName="Helvetica", fontSize=9.5, leading=14, spaceAfter=7, alignment=TA_JUSTIFY))
styles.add(ParagraphStyle(name="CellDF", fontName="Helvetica", fontSize=8, leading=11, spaceAfter=0))
styles.add(ParagraphStyle(name="HeadingDF", parent=styles["Heading2"], fontSize=14, leading=18, textColor=BLUE, spaceBefore=16, spaceAfter=9, keepWithNext=True))
styles.add(ParagraphStyle(name="SubDF", parent=styles["Heading3"], fontSize=11, leading=14, textColor=TEAL, spaceBefore=10, spaceAfter=7, keepWithNext=True))
styles.add(ParagraphStyle(name="CoverDF", fontName="Helvetica-Bold", fontSize=33, leading=38, textColor=BLUE, alignment=TA_CENTER, spaceAfter=18))
styles.add(ParagraphStyle(name="CenterDF", fontSize=12, leading=19, alignment=TA_CENTER, textColor=BLUE, spaceAfter=12))


def inline(text):
    text = html.escape(text.replace("\u2011", "-").replace("\u2013", "-").replace("\u2014", "-"))
    text = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", lambda m: '<font color="#137C83">' + m[1] + "</font>" if not m[2].startswith("http") else '<link href="' + m[2] + '">' + m[1] + "</link>", text)
    text = re.sub(r"\*\*(.+?)\*\*", r"<b>\1</b>", text)
    text = re.sub(r"`([^`]+)`", r'<font name="Courier" size="8">\1</font>', text)
    return text


def para(text, style="BodyDF"):
    return Paragraph(inline(text), styles[style])


def architecture():
    d = Drawing(WIDTH, 190)
    def box(x, y, w, h, label, fill):
        d.add(Rect(x, y, w, h, fillColor=fill, strokeColor=BLUE, strokeWidth=.7, rx=5, ry=5))
        d.add(String(x+w/2, y+h/2-3, label, textAnchor="middle", fontName="Helvetica-Bold", fontSize=9, fillColor=BLUE))
    def arrow(x1,y1,x2,y2):
        d.add(Line(x1,y1,x2,y2,strokeColor=TEAL,strokeWidth=1.2))
        d.add(Polygon([x2,y2,x2-4,y2+6,x2+4,y2+6],fillColor=TEAL,strokeColor=TEAL))
    box(175,153,130,30,"Cliente Python",colors.HexColor("#EAF1F7"))
    for x,label in ((25,"CN 01"),(185,"CN 02"),(345,"CN 03")):
        box(x,85,110,32,label,colors.HexColor("#DCECF4"))
    for x,label in ((25,"DN 01"),(185,"DN 02"),(345,"DN 03")):
        box(x,12,110,32,label,colors.HexColor("#D8F0EC"))
        arrow(x+55,83,x+55,46)
    arrow(240,151,240,120)
    d.add(Line(135,101,185,101,strokeColor=BLUE));d.add(Line(295,101,345,101,strokeColor=BLUE))
    d.add(String(240,126,"HTTPS: planes y metadatos",textAnchor="middle",fontSize=8,fillColor=BLUE))
    d.add(String(240,66,"Raft/mTLS entre controles; HTTPS binario con DataNodes",textAnchor="middle",fontSize=8,fillColor=BLUE))
    d.add(String(240,0,"Contenido directo cliente-DN; replicas y reparacion DN-DN",textAnchor="middle",fontSize=8,fillColor=BLUE))
    return d


def table(lines):
    rows = [[part.strip() for part in line.strip().strip("|").split("|")] for line in lines]
    rows = [row for row in rows if not all(re.fullmatch(r"[:\- ]+",cell) for cell in row)]
    n = len(rows[0])
    weights = [1/n]*n
    if n == 2: weights=[.3,.7]
    elif n == 3: weights=[.2,.38,.42]
    elif n == 4: weights=[.18,.29,.28,.25]
    data=[[para(cell,"CellDF") for cell in (row+[""]*n)[:n]] for row in rows]
    t=Table(data,colWidths=[WIDTH*w for w in weights],repeatRows=1,hAlign="LEFT")
    t.setStyle(TableStyle([("BACKGROUND",(0,0),(-1,0),colors.HexColor("#DFEAF2")),("ROWBACKGROUNDS",(0,1),(-1,-1),[colors.white,colors.HexColor("#F4F7F9")]),("VALIGN",(0,0),(-1,-1),"TOP"),("GRID",(0,0),(-1,-1),.4,colors.HexColor("#C9D5DC")),("LEFTPADDING",(0,0),(-1,-1),7),("RIGHTPADDING",(0,0),(-1,-1),7),("TOPPADDING",(0,0),(-1,-1),6),("BOTTOMPADDING",(0,0),(-1,-1),6)]))
    return t


def footer(canvas, doc):
    canvas.saveState();canvas.setStrokeColor(TEAL);canvas.line(2*cm,1.6*cm,A4[0]-2*cm,1.6*cm)
    canvas.setFont("Helvetica",8);canvas.setFillColor(BLUE);canvas.drawString(2*cm,1.15*cm,"DFSha | ST0263 / SI3007 | Universidad EAFIT | 2026-2")
    canvas.drawRightString(A4[0]-2*cm,1.15*cm,str(doc.page));canvas.restoreState()


def main():
    output=ROOT/"output/pdf/DFSha-informe-final.pdf";output.parent.mkdir(parents=True,exist_ok=True)
    doc=SimpleDocTemplate(str(output),pagesize=A4,rightMargin=2*cm,leftMargin=2*cm,topMargin=1.9*cm,bottomMargin=2*cm,title="DFSha - Informe tecnico final",author="Andres Velez Rendon y Jose Luis Restrepo")
    story=[Spacer(1,2.3*cm),para("DFSha","CoverDF"),para("Sistema de archivos distribuido","CenterDF"),para("Alta disponibilidad, rendimiento y seguridad","CenterDF"),Spacer(1,1.2*cm),architecture(),Spacer(1,1.2*cm),Paragraph("<b>Informe técnico final - Proyecto 1</b>",styles["CenterDF"]),Paragraph("Andrés Vélez Rendón<br/>Jose Luis Restrepo",styles["CenterDF"]),Paragraph("Universidad EAFIT<br/>ST0263 / SI3007 - Semestre 2026-2<br/>24 de octubre de 2026",styles["CenterDF"]),PageBreak()]
    lines=(ROOT/"docs/INFORME_FINAL.md").read_text(encoding="utf-8").splitlines()
    start=next(i for i,line in enumerate(lines) if line.startswith("## 1."));i=start
    while i<len(lines):
        line=lines[i].strip()
        if not line:i+=1;continue
        if line.startswith("## "):
            story.append(para(line[3:],"HeadingDF"));i+=1;continue
        if line.startswith("### "):
            story.append(para(line[4:],"SubDF"));i+=1;continue
        if line.startswith("|"):
            block=[]
            while i<len(lines) and lines[i].strip().startswith("|"):block.append(lines[i]);i+=1
            story.extend([table(block),Spacer(1,8)]);continue
        if line.startswith("```"):
            code=[];i+=1
            while i<len(lines) and not lines[i].strip().startswith("```"):code.append(lines[i]);i+=1
            story.append(Preformatted("\n".join(code),ParagraphStyle("CodeDF",fontName="Courier",fontSize=7,leading=10,backColor=colors.HexColor("#F2F5F7")),maxLineLength=96));i+=1;continue
        if line.startswith("- ") or re.match(r"\d+\. ",line):
            story.append(para(line));i+=1;continue
        block=[line];i+=1
        while i<len(lines) and lines[i].strip() and not lines[i].startswith(("#","|","```","- ")):
            block.append(lines[i].strip());i+=1
        story.append(para(" ".join(block)))
    doc.build(story,onFirstPage=footer,onLaterPages=footer)
    print(output)


if __name__=="__main__":main()
