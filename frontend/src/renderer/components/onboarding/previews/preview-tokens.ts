import type { CSSProperties } from "react";

/** The preview palette, copied from the landing app so onboarding owns its own
 *  copy. These are the dark-theme values from src/styles/tokens.css. */
export const featurePreviewTokens = {
	// Exact dark-theme values from frontend/src/styles/tokens.css (:root).
	"--preview-background": "oklch(0.185 0.006 285.885)",
	"--preview-foreground": "oklch(0.985 0 0)",
	"--preview-card": "oklch(0.24 0.008 285.885)",
	"--preview-card-foreground": "oklch(0.985 0 0)",
	"--preview-primary": "oklch(0.92 0.004 286.32)",
	"--preview-primary-foreground": "oklch(0.21 0.006 285.885)",
	"--preview-muted": "oklch(0.274 0.006 286.033)",
	"--preview-muted-foreground": "oklch(0.705 0.015 286.067)",
	"--preview-accent": "oklch(0.274 0.006 286.033)",
	"--preview-border": "oklch(1 0 0 / 7%)",
	"--preview-border-strong": "oklch(1 0 0 / 4%)",
	"--preview-ring": "oklch(0.552 0.016 285.938)",
	"--preview-divider": "oklch(1 0 0 / 4%)",
	"--preview-input": "oklch(1 0 0 / 4%)",
	"--preview-sidebar": "oklch(0.155 0.005 285.823)",
	"--preview-sidebar-foreground": "oklch(0.985 0 0)",
	"--preview-sidebar-accent": "oklch(0.274 0.006 286.033)",
	"--preview-sidebar-hover": "color-mix(in oklch, oklch(0.985 0 0) 4%, transparent)",
	"--preview-passive": "oklch(0.442 0.017 285.786)",
	"--preview-raised": "oklch(0.274 0.006 286.033)",
} as CSSProperties;
