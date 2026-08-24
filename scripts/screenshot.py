#!/usr/bin/env python3
"""Render a tmux ANSI capture (SGR truecolor/256/8) to a PNG screenshot.

Usage: screenshot.py <capture.txt> <out.png> [scale]

The capture must come from `tmux capture-pane -e -p` (one line per row,
escape sequences preserved). Rendering uses the same monospace family the
dashboard targets (Meslo LG), on the Catppuccin Mocha base the TUI paints.
"""

import os
import re
import sys

import pyte
from PIL import Image, ImageDraw, ImageFont

ANSI_RE = re.compile(rb"\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[()][A-Z0-9])")

BG = (30, 30, 46)  # #1e1e2e, matches internal/ui/theme.go cBase
FG_DEFAULT = (205, 214, 244)  # #cdd6f4
FONT_PATH = "/usr/share/fonts/TTF/MesloLGMDZNerdFont-Regular.ttf"
FONT_BOLD = "/usr/share/fonts/TTF/MesloLGMDZNerdFont-Bold.ttf"

# The 16 ANSI colors as SGR 30-37/90-97, tuned to the dashboard's palette.
ANSI16 = {
    0: (73, 77, 100),    # black-ish (surface)
    1: (243, 139, 168),  # red
    2: (166, 227, 161),  # green
    3: (249, 226, 175),  # yellow
    4: (137, 180, 250),  # blue
    5: (203, 166, 247),  # magenta
    6: (137, 220, 235),  # cyan
    7: (205, 214, 244),  # white
}


def clamp8(v):
    return max(0, min(255, v))


def sgr_rgb(color):
    if color is None:
        return None
    r, g, b = color
    return (clamp8(r), clamp8(g), clamp8(b))


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    src, out = sys.argv[1], sys.argv[2]
    scale = int(sys.argv[3]) if len(sys.argv) > 3 else 2
    # Exact pane geometry keeps pyte from wrapping or scrolling; pass the
    # values of #{pane_width} #{pane_height} from the capturing tmux session.
    cols = int(sys.argv[4]) if len(sys.argv) > 4 else 0
    rows = int(sys.argv[5]) if len(sys.argv) > 5 else 0

    with open(src, "rb") as f:
        data = f.read().rstrip(b"\n")

    lines = data.split(b"\n")
    if cols <= 0:
        # count runes after stripping escapes: braille dots are 3 UTF-8 bytes
        cols = max(len(ANSI_RE.sub(b"", l).decode("utf-8", "replace")) for l in lines)
    if rows <= 0:
        rows = len(lines)
    # Rejoin with CRLF: capture-pane trims trailing spaces, so bare \n would
    # start each row at the previous row's final column instead of col 0.
    text = b"\r\n".join(lines).decode("utf-8", errors="replace")
    screen = pyte.Screen(cols, rows)
    stream = pyte.Stream(screen)
    stream.feed(text)
    # feed() leaves the cursor on a final empty line when the capture ends in
    # a newline; rstrip above prevents that, so screen rows map 1:1.
    for y in range(rows):
        for x in range(cols):
            if screen.buffer[y][x].data == "" and x > 0:
                # inherit unwritten trailing cells from the line's last glyph
                screen.buffer[y][x].data = " "

    cell_w = 9 * scale
    cell_h = 19 * scale
    font_size = 16 * scale
    font = ImageFont.truetype(FONT_PATH, font_size)
    font_bold = ImageFont.truetype(FONT_BOLD, font_size)

    img = Image.new("RGB", (cols * cell_w, rows * cell_h), BG)
    draw = ImageDraw.Draw(img)

    for y in range(rows):
        line = screen.buffer[y]
        x = 0
        while x < cols:
            ch = line[x].data
            if ch == " " and line[x].bg is None and line[x].fg is None:
                x += 1
                continue
            fg = sgr_rgb(ansi_or_truecolor(line[x].fg, True)) or FG_DEFAULT
            bg = sgr_rgb(ansi_or_truecolor(line[x].bg, False))
            bold = line[x].bold
            if bg is not None:
                draw.rectangle(
                    [x * cell_w, y * cell_h, (x + 1) * cell_w - 1, (y + 1) * cell_h - 1],
                    fill=bg,
                )
            # grapheme clusters: consume combining marks / wide sequences
            cluster = ch
            width = 1
            if x + 1 < cols and line[x + 1].data in ("", "\ufe0f"):
                pass
            f = font_bold if bold else font
            draw.text(
                (x * cell_w + cell_w // 2, y * cell_h + cell_h // 2),
                cluster,
                font=f,
                fill=fg,
                anchor="mm",
            )
            x += width

    img.save(out)
    print(f"{out}: {img.width}x{img.height} from {cols}x{rows} cells")


def ansi_or_truecolor(color, is_fg):
    """Map pyte color names to RGB tuples."""
    if color is None:
        return None
    v = color.lstrip("#")
    if len(v) == 6 and all(c in "0123456789abcdefABCDEF" for c in v):
        return tuple(int(v[i : i + 2], 16) for i in (0, 2, 4))
    named = {
        "black": 0, "red": 1, "green": 2, "brown": 3, "blue": 4,
        "magenta": 5, "cyan": 6, "white": 7,
        "brightblack": 8, "brightred": 9, "brightgreen": 10,
        "brightbrown": 11, "brightyellow": 11, "brightblue": 12,
        "brightmagenta": 13, "brightcyan": 14, "brightwhite": 15,
        "default": None,
    }
    key = color.lower().replace("light", "bright")
    idx = named.get(key)
    if idx is None:
        return None
    if idx < 8:
        return ANSI16[idx]
    # bright variants reuse the palette
    return ANSI16[idx - 8]


if __name__ == "__main__":
    main()
