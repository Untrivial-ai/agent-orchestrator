#!/usr/bin/env python3
from __future__ import annotations

from pathlib import Path
from typing import Iterable

from PIL import Image, ImageDraw, ImageFilter, ImageFont, ImageOps


ROOT = Path(__file__).resolve().parents[1]
SOURCE_DIR = Path("/Users/prasadware/Desktop/app-screenshots")
OUT_DIR = ROOT / "packages/mobile/store-assets/screenshots/en"
ICON = ROOT / "packages/mobile/assets/icon.png"

CAPTURES = [
    {
        "src": SOURCE_DIR / "IMG_2245.PNG",
        "name": "01-agents",
        "caption": "Control agent work from your phone",
        "sub": "Track live sessions, merge state, and what needs attention.",
    },
    {
        "src": SOURCE_DIR / "IMG_2246.PNG",
        "name": "02-orchestrator",
        "caption": "Coordinate every repo in one place",
        "sub": "See each project fleet and jump back into the terminal.",
    },
    {
        "src": SOURCE_DIR / "IMG_2251.PNG",
        "name": "03-notifications",
        "caption": "Know when work needs you",
        "sub": "PRs, blocked agents, and review-ready work stay visible.",
    },
]

TARGETS = [
    ("ios-6.7", 1290, 2796, "portrait"),
    ("ios-6.5", 1284, 2778, "portrait"),
    ("android-portrait", 1080, 1920, "portrait"),
    ("android-landscape", 1920, 1080, "landscape"),
]


def font(size: int, weight: str = "regular") -> ImageFont.FreeTypeFont:
    candidates = [
        "/System/Library/Fonts/SFNS.ttf",
        "/System/Library/Fonts/HelveticaNeue.ttc",
        "/System/Library/Fonts/Supplemental/Arial Bold.ttf" if weight == "bold" else "/System/Library/Fonts/Supplemental/Arial.ttf",
    ]
    for path in candidates:
        try:
            return ImageFont.truetype(path, size=size, index=0)
        except OSError:
            continue
    return ImageFont.load_default(size=size)


def rounded_rect_mask(size: tuple[int, int], radius: int) -> Image.Image:
    mask = Image.new("L", size, 0)
    ImageDraw.Draw(mask).rounded_rectangle((0, 0, size[0] - 1, size[1] - 1), radius=radius, fill=255)
    return mask


def resize_cover(img: Image.Image, size: tuple[int, int]) -> Image.Image:
    return ImageOps.fit(img, size, method=Image.Resampling.LANCZOS, centering=(0.5, 0.5))


def draw_gradient(size: tuple[int, int]) -> Image.Image:
    w, h = size
    base = Image.new("RGB", size, "#090b10")
    px = base.load()
    for y in range(h):
        for x in range(w):
            nx = x / max(w - 1, 1)
            ny = y / max(h - 1, 1)
            blue = int(12 + 26 * nx + 18 * (1 - ny))
            green = int(11 + 15 * (1 - nx) + 18 * ny)
            red = int(7 + 12 * ny)
            px[x, y] = (red, green, blue)
    return base


def wrap_text(draw: ImageDraw.ImageDraw, text: str, face: ImageFont.FreeTypeFont, max_width: int) -> list[str]:
    words = text.split()
    lines: list[str] = []
    current = ""
    for word in words:
        candidate = word if not current else f"{current} {word}"
        if draw.textbbox((0, 0), candidate, font=face)[2] <= max_width:
            current = candidate
        else:
            if current:
                lines.append(current)
            current = word
    if current:
        lines.append(current)
    return lines[:2]


def paste_phone(canvas: Image.Image, screenshot: Image.Image, box: tuple[int, int, int, int], radius: int) -> None:
    x, y, w, h = box
    shadow = Image.new("RGBA", (w + 90, h + 90), (0, 0, 0, 0))
    sd = ImageDraw.Draw(shadow)
    sd.rounded_rectangle((45, 45, w + 45, h + 45), radius=radius + 40, fill=(0, 0, 0, 170))
    shadow = shadow.filter(ImageFilter.GaussianBlur(28))
    canvas.alpha_composite(shadow, (x - 45, y - 20))

    frame = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    fd = ImageDraw.Draw(frame)
    fd.rounded_rectangle((0, 0, w - 1, h - 1), radius=radius, fill="#090a0d", outline="#30343d", width=max(8, w // 55))
    inner_pad = max(16, w // 32)
    inner = (inner_pad, inner_pad, w - inner_pad, h - inner_pad)
    shot = resize_cover(screenshot, (inner[2] - inner[0], inner[3] - inner[1]))
    frame.alpha_composite(shot.convert("RGBA"), (inner[0], inner[1]))
    mask = rounded_rect_mask((inner[2] - inner[0], inner[3] - inner[1]), max(24, radius - inner_pad))
    clipped = Image.new("RGBA", frame.size, (0, 0, 0, 0))
    clipped.paste(shot.convert("RGBA"), (inner[0], inner[1]), mask)
    frame.alpha_composite(clipped)
    canvas.alpha_composite(frame, (x, y))


def draw_caption(draw: ImageDraw.ImageDraw, xy: tuple[int, int], max_width: int, caption: str, sub: str, scale: float) -> int:
    x, y = xy
    title_font = font(int(72 * scale), "bold")
    sub_font = font(int(30 * scale))
    lines = wrap_text(draw, caption, title_font, max_width)
    for line in lines:
        draw.text((x, y), line, font=title_font, fill="#f6f8ff")
        y += int(86 * scale)
    y += int(18 * scale)
    for line in wrap_text(draw, sub, sub_font, max_width):
        draw.text((x, y), line, font=sub_font, fill="#aeb6c8")
        y += int(42 * scale)
    return y


def draw_brand(draw: ImageDraw.ImageDraw, canvas: Image.Image, x: int, y: int, scale: float) -> None:
    icon_size = int(64 * scale)
    if ICON.exists():
        icon = Image.open(ICON).convert("RGBA").resize((icon_size, icon_size), Image.Resampling.LANCZOS)
        canvas.alpha_composite(icon, (x, y))
    draw.text((x + icon_size + int(18 * scale), y + int(8 * scale)), "AO - Mobile", font=font(int(34 * scale), "bold"), fill="#f6f8ff")


def portrait(target: str, w: int, h: int, item: dict[str, str]) -> Image.Image:
    canvas = draw_gradient((w, h)).convert("RGBA")
    draw = ImageDraw.Draw(canvas)
    scale = w / 1290
    margin = int(96 * scale)
    draw_brand(draw, canvas, margin, int(72 * scale), scale)
    draw_caption(draw, (margin, int(190 * scale)), w - margin * 2, item["caption"], item["sub"], scale)
    shot = Image.open(item["src"]).convert("RGB")
    phone_w = int(w * 0.68)
    phone_h = int(phone_w * 2532 / 1170)
    max_h = h - int(680 * scale)
    if phone_h > max_h:
        phone_h = max_h
        phone_w = int(phone_h * 1170 / 2532)
    x = (w - phone_w) // 2
    y = h - phone_h - int(96 * scale)
    paste_phone(canvas, shot, (x, y, phone_w, phone_h), int(92 * scale))
    return canvas


def landscape(target: str, w: int, h: int, item: dict[str, str]) -> Image.Image:
    canvas = draw_gradient((w, h)).convert("RGBA")
    draw = ImageDraw.Draw(canvas)
    scale = h / 1080
    margin = int(96 * scale)
    draw_brand(draw, canvas, margin, int(74 * scale), scale)
    draw_caption(draw, (margin, int(230 * scale)), int(w * 0.43), item["caption"], item["sub"], scale)
    shot = Image.open(item["src"]).convert("RGB")
    phone_h = int(h * 0.82)
    phone_w = int(phone_h * 1170 / 2532)
    paste_phone(canvas, shot, (w - phone_w - int(150 * scale), int(90 * scale), phone_w, phone_h), int(70 * scale))
    return canvas


def save_feature_graphic() -> None:
    w, h = 1024, 500
    canvas = draw_gradient((w, h)).convert("RGBA")
    draw = ImageDraw.Draw(canvas)
    draw_brand(draw, canvas, 56, 48, 0.9)
    draw.text((56, 154), "Run your agent fleet", font=font(58, "bold"), fill="#f6f8ff")
    draw.text((56, 226), "from your phone", font=font(58, "bold"), fill="#f6f8ff")
    draw.text((58, 322), "Live sessions, PR state, and notifications wherever you are.", font=font(26), fill="#aeb6c8")
    shot = Image.open(CAPTURES[0]["src"]).convert("RGB")
    paste_phone(canvas, shot, (702, 34, 230, 498), 36)
    canvas.convert("RGB").save(OUT_DIR / "google-play-feature-graphic-1024x500.png", optimize=True)


def main() -> None:
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    missing = [str(item["src"]) for item in CAPTURES if not item["src"].exists()]
    if missing:
        raise SystemExit("Missing source screenshots:\n" + "\n".join(missing))

    for target, w, h, mode in TARGETS:
        for item in CAPTURES:
            image = portrait(target, w, h, item) if mode == "portrait" else landscape(target, w, h, item)
            image.convert("RGB").save(OUT_DIR / f"{target}-{item['name']}-{w}x{h}.png", optimize=True)
    save_feature_graphic()


if __name__ == "__main__":
    main()
