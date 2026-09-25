// Pierre derives its canvas from the selected Shiki theme and injects that
// value inside its shadow root. Keep the theme's syntax colors, but use AO's
// own workspace canvas for the visible file and diff surfaces.
export const AO_PIERRE_SURFACE_CSS = `
:host {
	--diffs-bg: var(--color-bg-primary);
}

/* Filler rows (the empty side of a split hunk, and the row an inline feedback
   composer opens in) keep their content treatment, but their line-number
   gutter should not paint a grey block; let it sit on the canvas like the
   other gutters. */
[data-gutter-buffer="buffer"][data-gutter-buffer] {
	--diffs-line-bg: var(--diffs-bg);
}

[data-gutter-buffer="annotation"][data-gutter-buffer] {
	--diffs-annotation-bg: var(--diffs-bg);
}
`;
