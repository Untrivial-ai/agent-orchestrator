#!/usr/bin/env python3
from __future__ import annotations

from pathlib import Path

from PIL import Image, ImageDraw, ImageFilter, ImageFont, ImageOps


ROOT = Path(__file__).resolve().parents[1]
SOURCE_DIR = Path("/Users/prasadware/Desktop/app-screenshots")
OUT_DIR = ROOT / "packages/mobile/store-assets/screenshots/inference-sh/en"
ICON = ROOT / "packages/mobile/assets/icon.png"

CAPTURES = [
    {
        "src": SOURCE_DIR / "IMG_2245.PNG",
        "name": "01-agents",
        "caption": "Run agents from anywhere",
        "sub": "Live status, project context, and merge progress in your pocket.",
        "callout": "Live session health",
    },
    {
        "src": SOURCE_DIR / "IMG_2246.PNG",
        "name": "02-orchestrator",
        "caption": "Coordinate every project",
        "sub": "Jump into repo fleets, workers, and terminals without returning to your desk.",
        "callout": "One view for all repos",
    },
    {
        "src": SOURCE_DIR / "IMG_2251.PNG",
        "name": "03-notifications",
        "caption": "Catch the work that needs you",
        "sub": "Agent prompts, PR updates, and merge-ready moments stay visible.",
        "callout": "Actionable alerts",
    },
]

TARGETS = [
    ("ios-6.7", 1290, 2796, "portrait"),
    ("ios-6.5", 1284, 2778, "portrait"),
    ("android-portrait", 1080, 1920, "portrait"),
    ("android-landscape", 1920, 1080, "landscape"),
]


def font(size: int, weight: str = "regular") -> ImageFont.FreeTypeFont:
    paths = [
        "/System/Library/Fonts/SFNS.ttf",
        "/System/Library/Fonts/HelveticaNeue.ttc",
        "/System/Library/Fonts/Supplemental/Arial Bold.ttf" if weight == "bold" else "/System/Library/Fonts/Supplemental/Arial.ttf",
    ]
    for path in paths:
        try:
            return ImageFont.truetype(path, size=size, index=0)
        except OSError:
            pass
    return ImageFont.load_default(size=size)


def fit(img: Image.Image, size: tuple[int, int]) -> Image.Image:
    return ImageOps.fit(img, size, method=Image.Resampling.LANCZOS, centering=(0.5, 0.5))


def background(size: tuple[int, int]) -> Image.Image:
    w, h = size
    img = Image.new("RGB", size)
    px = img.load()
    for y in range(h):
        for x in range(w):
            nx = x / max(w - 1, 1)
            ny = y / max(h - 1, 1)
            px[x, y] = (
                int(238 - 26 * ny + 8 * nx),
                int(242 - 30 * nx - 18 * ny),
                int(236 - 40 * nx + 14 * ny),
            )
    return img.convert("RGBA")


def mask(size: tuple[int, int], radius: int) -> Image.Image:
    out = Image.new("L", size, 0)
    ImageDraw.Draw(out).rounded_rectangle((0, 0, size[0] - 1, size[1] - 1), radius=radius, fill=255)
    return out


def text_lines(draw: ImageDraw.ImageDraw, text: str, face: ImageFont.FreeTypeFont, max_width: int) -> list[str]:
    lines: list[str] = []
    line = ""
    for word in text.split():
        candidate = word if not line else f"{line} {word}"
        if draw.textbbox((0, 0), candidate, font=face)[2] <= max_width:
            line = candidate
            continue
        if line:
            lines.append(line)
        line = word
    if line:
        lines.append(line)
    return lines[:2]


def phone(canvas: Image.Image, shot: Image.Image, box: tuple[int, int, int, int], radius: int) -> None:
    x, y, w, h = box
    shadow = Image.new("RGBA", (w + 96, h + 96), (0, 0, 0, 0))
    sd = ImageDraw.Draw(shadow)
    sd.rounded_rectangle((48, 48, w + 48, h + 48), radius=radius + 34, fill=(18, 24, 34, 120))
    canvas.alpha_composite(shadow.filter(ImageFilter.GaussianBlur(26)), (x - 48, y - 28))

    frame = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    fd = ImageDraw.Draw(frame)
    border = max(8, w // 54)
    fd.rounded_rectangle((0, 0, w - 1, h - 1), radius=radius, fill="#08090c", outline="#1d2430", width=border)
    pad = max(14, w // 31)
    inner_size = (w - pad * 2, h - pad * 2)
    inner = fit(shot, inner_size).convert("RGBA")
    clipped = Image.new("RGBA", frame.size, (0, 0, 0, 0))
    clipped.paste(inner, (pad, pad), mask(inner_size, max(22, radius - pad)))
    frame.alpha_composite(clipped)
    canvas.alpha_composite(frame, (x, y))


def brand(canvas: Image.Image, draw: ImageDraw.ImageDraw, x: int, y: int, scale: float, ink: str) -> None:
    size = int(58 * scale)
    if ICON.exists():
        icon = Image.open(ICON).convert("RGBA").resize((size, size), Image.Resampling.LANCZOS)
        canvas.alpha_composite(icon, (x, y))
    draw.text((x + size + int(16 * scale), y + int(7 * scale)), "AO - Mobile", font=font(int(32 * scale), "bold"), fill=ink)


def caption(draw: ImageDraw.ImageDraw, x: int, y: int, width: int, item: dict[str, str], scale: float, ink: str) -> int:
    title = font(int(78 * scale), "bold")
    sub = font(int(30 * scale))
    for line in text_lines(draw, item["caption"], title, width):
        draw.text((x, y), line, font=title, fill=ink)
        y += int(90 * scale)
    y += int(12 * scale)
    for line in text_lines(draw, item["sub"], sub, width):
        draw.text((x, y), line, font=sub, fill="#526070")
        y += int(42 * scale)
    return y


def callout(canvas: Image.Image, draw: ImageDraw.ImageDraw, text: str, xy: tuple[int, int], scale: float) -> None:
    x, y = xy
    face = font(int(28 * scale), "bold")
    pad_x = int(22 * scale)
    pad_y = int(14 * scale)
    bbox = draw.textbbox((0, 0), text, font=face)
    w = bbox[2] + pad_x * 2
    h = bbox[3] + pad_y * 2
    layer = Image.new("RGBA", canvas.size, (0, 0, 0, 0))
    ld = ImageDraw.Draw(layer)
    ld.rounded_rectangle((x, y, x + w, y + h), radius=int(24 * scale), fill=(255, 255, 255, 238), outline=(72, 99, 135, 80), width=max(1, int(2 * scale)))
    ld.text((x + pad_x, y + pad_y - int(2 * scale)), text, font=face, fill="#111827")
    canvas.alpha_composite(layer)


def make_portrait(w: int, h: int, item: dict[str, str]) -> Image.Image:
    canvas = background((w, h))
    draw = ImageDraw.Draw(canvas)
    scale = w / 1290
    margin = int(84 * scale)
    brand(canvas, draw, margin, int(68 * scale), scale, "#111827")
    caption(draw, margin, int(178 * scale), w - margin * 2, item, scale, "#111827")
    shot = Image.open(item["src"]).convert("RGB")
    phone_w = int(w * 0.72)
    phone_h = int(phone_w * 2532 / 1170)
    max_h = h - int(620 * scale)
    if phone_h > max_h:
        phone_h = max_h
        phone_w = int(phone_h * 1170 / 2532)
    x = (w - phone_w) // 2
    y = h - phone_h - int(88 * scale)
    phone(canvas, shot, (x, y, phone_w, phone_h), int(96 * scale))
    callout(canvas, draw, item["callout"], (x + int(phone_w * 0.48), y + int(phone_h * 0.16)), scale)
    return canvas


def make_landscape(w: int, h: int, item: dict[str, str]) -> Image.Image:
    canvas = background((w, h))
    draw = ImageDraw.Draw(canvas)
    scale = h / 1080
    margin = int(84 * scale)
    brand(canvas, draw, margin, int(70 * scale), scale, "#111827")
    caption(draw, margin, int(226 * scale), int(w * 0.45), item, scale, "#111827")
    shot = Image.open(item["src"]).convert("RGB")
    phone_h = int(h * 0.84)
    phone_w = int(phone_h * 1170 / 2532)
    x = w - phone_w - int(162 * scale)
    y = int(78 * scale)
    phone(canvas, shot, (x, y, phone_w, phone_h), int(72 * scale))
    callout(canvas, draw, item["callout"], (x - int(130 * scale), y + int(phone_h * 0.28)), scale)
    return canvas


def feature_graphic() -> Image.Image:
    canvas = background((1024, 500))
    draw = ImageDraw.Draw(canvas)
    brand(canvas, draw, 52, 44, 0.85, "#111827")
    draw.text((52, 150), "Your agent fleet,", font=font(54, "bold"), fill="#111827")
    draw.text((52, 214), "now on mobile", font=font(54, "bold"), fill="#111827")
    draw.text((54, 314), "Coordinate sessions, PRs, and prompts from AO - Mobile.", font=font(25), fill="#526070")
    shot = Image.open(CAPTURES[1]["src"]).convert("RGB")
    phone(canvas, shot, (728, 28, 220, 476), 36)
    return canvas


def main() -> None:
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    missing = [str(item["src"]) for item in CAPTURES if not item["src"].exists()]
    if missing:
        raise SystemExit("Missing source screenshots:\n" + "\n".join(missing))

    for target, w, h, mode in TARGETS:
        for item in CAPTURES:
            img = make_portrait(w, h, item) if mode == "portrait" else make_landscape(w, h, item)
            img.convert("RGB").save(OUT_DIR / f"{target}-{item['name']}-{w}x{h}.png", optimize=True)
    feature_graphic().convert("RGB").save(OUT_DIR / "google-play-feature-graphic-1024x500.png", optimize=True)


if __name__ == "__main__":
    main()
