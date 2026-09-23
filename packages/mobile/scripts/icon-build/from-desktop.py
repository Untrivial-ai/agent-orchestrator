"""Derive every mobile icon asset from the desktop app icon.

    python3 from-desktop.py <frontend/assets/icon.png> <out-dir>

The desktop icon (frontend/assets/icon.png, also packed into icon.icns / icon.ico)
is a pre-rendered macOS tile: Ruto on a black rounded square with a bevelled rim
and a transparent margin. Mobile platforms draw their own tile shape, so the
margin, rim and rounded corners have to go while the tile's face and Ruto stay
exactly as the desktop shows them. This script:

1. Fits the tile face's lighting (a smooth diagonal shade, lighter top-left) as
   a quadratic per channel, sampled away from the rim and from Ruto.
2. Un-mixes Ruto -- including its soft drop shadow -- from that background into
   a straight-alpha layer, so compositing it back over the fitted face
   reproduces the desktop pixels.
3. Re-lays both onto each platform's canvas, mapping the tile's outer edge to
   the platform's icon mask so Ruto keeps the same size and position as on the
   desktop tile.

Outputs (1024px unless noted):
  icon.png                     full-bleed square, iOS <=18 / Expo Go / Android legacy
  AO.icon/                     Icon Composer bundle for iOS 26: Ruto over the face
  android-icon-background.png  adaptive-icon background (the face shade)
  android-icon-foreground.png  adaptive-icon foreground, Ruto inside the safe circle
  android-icon-monochrome.png  Android 13+ themed-icon silhouette
  splash-icon.png              Ruto on transparent, for the splash screen
  favicon.png (256px)          the desktop tile itself, downscaled
"""
import json
import sys
from pathlib import Path

from PIL import Image, ImageFilter

SIZE = 1024
# Android keeps only the central 66dp of the 108dp adaptive canvas under every
# mask shape, and shows roughly the central 72dp as the tile.
ANDROID_SAFE = 66 / 108
ANDROID_VIEWPORT = 72 / 108
# Un-mix thresholds: colour distance from the fitted face below LO is face noise,
# above HI is fully Ruto.
LO, HI = 6.0, 60.0
# The splash has no mask to crop into, so the tile is drawn smaller there, leaving
# Ruto about as wide as the previous splash artwork.
SPLASH_TILE = 0.80


def tile_rect(src):
    """Outer edge of the opaque tile, measured through its centre lines."""
    a = src.getchannel("A").load()
    w, h = src.size
    cy, cx = h // 2, w // 2
    left = next(x for x in range(w) if a[x, cy] >= 128)
    right = next(x for x in range(w - 1, -1, -1) if a[x, cy] >= 128) + 1
    top = next(y for y in range(h) if a[cx, y] >= 128)
    # The tile's 3D lip adds depth below the face; keep the tile square.
    side = right - left
    return left, top, side


def solve(m, v):
    """Gaussian elimination for the small normal-equation systems."""
    n = len(v)
    m = [row[:] + [v[i]] for i, row in enumerate(m)]
    for c in range(n):
        p = max(range(c, n), key=lambda r: abs(m[r][c]))
        m[c], m[p] = m[p], m[c]
        for r in range(n):
            if r != c and m[c][c]:
                f = m[r][c] / m[c][c]
                m[r] = [a - f * b for a, b in zip(m[r], m[c])]
    return [m[i][n] / m[i][i] for i in range(n)]


def basis(u, v):
    return (1.0, u, v, u * u, u * v, v * v)


def fit_face(src, rect):
    """Quadratic per channel over the tile face, in tile-relative coords (0..1)."""
    left, top, side = rect
    px = src.load()
    # Anything noticeably brighter or more saturated than the dark face is Ruto
    # (or the rim); grow it so shadows and antialiasing stay out of the fit.
    rough = Image.new("L", src.size, 0)
    rp = rough.load()
    for y in range(src.size[1]):
        for x in range(src.size[0]):
            r, g, b, a = px[x, y]
            if a < 255 or max(r, g, b) > 70 or max(r, g, b) - min(r, g, b) > 12:
                rp[x, y] = 255
    rough = rough.filter(ImageFilter.MaxFilter(41))
    rq = rough.load()
    inset = side * 0.06  # clear of the bevelled rim
    ata = [[0.0] * 6 for _ in range(6)]
    atb = [[0.0] * 6 for _ in range(3)]
    for y in range(int(top + inset), int(top + side - inset), 3):
        for x in range(int(left + inset), int(left + side - inset), 3):
            if rq[x, y]:
                continue
            f = basis((x - left) / side, (y - top) / side)
            for i in range(6):
                for j in range(6):
                    ata[i][j] += f[i] * f[j]
                for c in range(3):
                    atb[c][i] += f[i] * px[x, y][c]
    return [solve(ata, atb[c]) for c in range(3)]


def face_at(coef, u, v):
    f = basis(u, v)
    return tuple(min(255.0, max(0.0, sum(k * b for k, b in zip(coef[c], f)))) for c in range(3))


def face_image(coef, size, map_uv):
    img = Image.new("RGB", (size, size))
    d = img.load()
    for y in range(size):
        for x in range(size):
            d[x, y] = tuple(round(c) for c in face_at(coef, *map_uv(x + 0.5, y + 0.5)))
    return img


def ruto_box(src, rect, pad=0.04):
    """Bounding box of Ruto's saturated body and bright wand, padded for shadow."""
    left, top, side = rect
    px = src.load()
    w, h = src.size
    strong = Image.new("L", src.size, 0)
    sp = strong.load()
    for y in range(h):
        for x in range(w):
            r, g, b, a = px[x, y]
            if a == 255 and (max(r, g, b) - min(r, g, b) > 60 or min(r, g, b) > 150):
                sp[x, y] = 255
    # The rim's highlight is bright too, so keep only what connects to Ruto's
    # body (the wand touches the raised hand). Seed from the tile centre, which
    # is Ruto's torso.
    sp = strong.filter(ImageFilter.MaxFilter(5)).load()
    seed = (left + side // 2, top + side // 2)
    assert sp[seed], "tile centre is not on Ruto; artwork changed?"
    seen, stack, xs, ys = {seed}, [seed], [], []
    while stack:
        x, y = stack.pop()
        xs.append(x)
        ys.append(y)
        for n in ((x + 1, y), (x - 1, y), (x, y + 1), (x, y - 1)):
            if 0 <= n[0] < w and 0 <= n[1] < h and n not in seen and sp[n]:
                seen.add(n)
                stack.append(n)
    p = round(side * pad)
    return min(xs) - p, min(ys) - p, max(xs) + 1 + p, max(ys) + 1 + p


def unmix(src, rect, coef):
    """Straight-alpha Ruto layer (with drop shadow), in source pixel space."""
    left, top, side = rect
    px = src.load()
    out = Image.new("RGBA", src.size, (0, 0, 0, 0))
    op = out.load()
    # Work only in a box around Ruto: the rim's highlight curves inward at the
    # rounded corners and would otherwise be mistaken for artwork.
    x0, y0, x1, y1 = ruto_box(src, rect)
    for y in range(y0, y1):
        for x in range(x0, x1):
            r, g, b, _ = px[x, y]
            bg = face_at(coef, (x - left) / side, (y - top) / side)
            d = ((r - bg[0]) ** 2 + (g - bg[1]) ** 2 + (b - bg[2]) ** 2) ** 0.5
            if d <= LO:
                continue
            darker = r <= bg[0] and g <= bg[1] and b <= bg[2]
            if darker and max(r, g, b) - min(r, g, b) < 8:
                # Neutral and darker than the face: shadow, i.e. black at partial alpha.
                lum_p, lum_b = (r + g + b) / 3, sum(bg) / 3
                a = 1.0 - lum_p / max(lum_b, 1.0)
                op[x, y] = (0, 0, 0, round(255 * min(1.0, a)))
                continue
            a = min(1.0, (d - LO) / (HI - LO))
            fg = tuple(round(min(255.0, max(0.0, (p - (1 - a) * q) / a))) for p, q in zip((r, g, b), bg))
            op[x, y] = fg + (round(255 * a),)
    return out


def place(layer, rect, size, tile_px, centre=None):
    """Scale the source tile to `tile_px` and centre it on a `size` canvas."""
    left, top, side = rect
    scale = tile_px / side
    scaled = layer.resize((round(layer.size[0] * scale), round(layer.size[1] * scale)), Image.LANCZOS)
    cx = centre if centre is not None else size / 2
    off = (round(cx - (left + side / 2) * scale), round(cx - (top + side / 2) * scale))
    canvas = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    canvas.alpha_composite(scaled, (max(0, off[0]), max(0, off[1])), (max(0, -off[0]), max(0, -off[1])))
    return canvas


def content_radius(layer, alpha_min=64):
    a = layer.getchannel("A").load()
    w, h = layer.size
    box = layer.getchannel("A").point(lambda v: 255 if v >= alpha_min else 0).getbbox()
    cx, cy = w / 2, h / 2
    return max(
        ((x + 0.5 - cx) ** 2 + (y + 0.5 - cy) ** 2) ** 0.5
        for y in range(box[1], box[3])
        for x in range(box[0], box[2])
        if a[x, y] >= alpha_min
    )


def srgb(c):
    return "srgb:" + ",".join(f"{v / 255:.5f}" for v in c) + ",1.00000"


def main():
    src = Image.open(sys.argv[1]).convert("RGBA")
    out = Path(sys.argv[2])
    (out / "AO.icon" / "Assets").mkdir(parents=True, exist_ok=True)

    rect = tile_rect(src)
    coef = fit_face(src, rect)
    ruto = unmix(src, rect, coef)

    # --- iOS / Expo Go / legacy: tile edge -> icon mask edge ------------------
    face = face_image(coef, SIZE, lambda x, y: (x / SIZE, y / SIZE))
    ruto_ios = place(ruto, rect, SIZE, SIZE)
    icon = face.convert("RGBA")
    icon.alpha_composite(ruto_ios)
    icon.convert("RGB").save(out / "icon.png")
    ruto_ios.save(out / "AO.icon" / "Assets" / "ruto.png")
    face.save(out / "AO.icon" / "Assets" / "face.png")

    # Icon Composer ignores a linear-gradient fill's orientation and always runs
    # it top to bottom, so it cannot reproduce the face's diagonal shade. The
    # face goes in as the bottom image layer instead (ictool renders of Default
    # and Dark then match icon.png to within 1/255), hidden in the tinted
    # appearances so iOS draws its own tinted tile there. The fill underneath is
    # the face's mean, seen only in clear/tinted modes.
    mean = tuple(sum(c) / (SIZE * SIZE) for c in zip(*face.getdata()))
    no_effects = {
        "shadow": {"kind": "none", "opacity": 0.0},
        "specular": False,
        "translucency": {"enabled": False, "value": 0.5},
    }
    icon_json = {
        "fill": {"solid": srgb(mean)},
        "groups": [
            # Ruto already carries the desktop's bevel, gloss and drop shadow.
            # The system's specular and shadow would stack on top and wash the
            # blue out (measured 55,180,249 -> 94,178,243), so both are off.
            {"layers": [{"image-name": "ruto.png", "name": "Ruto"}], **no_effects},
            {
                "layers": [
                    {
                        "image-name": "face.png",
                        "name": "Tile face",
                        "hidden-specializations": [{"appearance": "tinted", "value": True}],
                    }
                ],
                **no_effects,
            },
        ],
        "supported-platforms": {"circles": ["watchOS"], "squares": ["iOS", "macOS"]},
    }
    (out / "AO.icon" / "icon.json").write_text(json.dumps(icon_json, indent=2) + "\n")

    # --- Android adaptive: tile -> 72dp viewport, shrunk to the 66dp circle ---
    tile_px = SIZE * ANDROID_VIEWPORT
    fg = place(ruto, rect, SIZE, tile_px)
    limit = SIZE * ANDROID_SAFE / 2
    r = content_radius(fg)
    if r > limit:
        tile_px *= limit / r
        fg = place(ruto, rect, SIZE, tile_px)
    fg.save(out / "android-icon-foreground.png")
    view = SIZE * ANDROID_VIEWPORT
    lo = (SIZE - view) / 2
    face_image(coef, SIZE, lambda x, y: ((x - lo) / view, (y - lo) / view)).save(
        out / "android-icon-background.png"
    )
    silhouette = fg.getchannel("A").point(lambda v: 255 if v >= 128 else 0)
    # Keep Ruto's body, not the shadow: shadow pixels are black.
    fp = fg.load()
    sp = silhouette.load()
    for y in range(SIZE):
        for x in range(SIZE):
            if sp[x, y] and max(fp[x, y][:3]) < 40:
                sp[x, y] = 0
    mono = Image.new("RGBA", (SIZE, SIZE), (255, 255, 255, 0))
    mono.putalpha(silhouette.filter(ImageFilter.GaussianBlur(0.6)))
    mono.save(out / "android-icon-monochrome.png")

    # --- Splash: Ruto on transparent over app.json's splash background -------
    place(ruto, rect, SIZE, SIZE * SPLASH_TILE).save(out / "splash-icon.png")

    # --- Web favicon: the desktop tile as-is ---------------------------------
    src.resize((256, 256), Image.LANCZOS).save(out / "favicon.png")

    print("tile", rect, "-> android tile", round(tile_px), "px")


if __name__ == "__main__":
    main()
