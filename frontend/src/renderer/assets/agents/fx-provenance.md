# fx logo

The SVG uses the official `logo-fx-glyph` path from the header at
<https://fx.sh/> (Vercel Labs, <https://github.com/vercel-labs/fx>), retrieved
2026-09-15. Its path geometry and viewBox are unchanged. Website hover animation
was removed, and black/white foreground colors follow the viewer's color scheme.
This is an upstream brand asset, not an AI-generated interpretation.

The mobile asset at `packages/mobile/assets/agents/fx.png` is the 128 × 128 PNG
rasterization of this SVG's black variant, with transparency preserved:

```sh
sips -s format png -z 128 128 frontend/src/renderer/assets/agents/fx.svg --out packages/mobile/assets/agents/fx.png
```

All visible mobile pixels are black, so the existing mobile logo registry gives
fx a light backdrop for contrast.
