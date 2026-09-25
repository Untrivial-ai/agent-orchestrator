// Pierre derives its canvas from the selected Shiki theme and injects that
// value inside its shadow root. Keep the theme's syntax colors, but use AO's
// own workspace canvas for the visible file and diff surfaces.
export const AO_PIERRE_SURFACE_CSS = `
:host {
	--diffs-bg: var(--color-bg-primary);
}

/* Split diff with an inline composer open: the opposite side's annotation cell
   (no slot, so nothing mounted in it) reads as filler, like the empty side of a
   split hunk — Pierre's [data-content-buffer] stripes on the canvas instead of
   a solid grey block. */
[data-line-annotation][data-line-annotation]:not(:has(slot)) {
	--diffs-annotation-bg: var(--diffs-bg);
	background-image: repeating-linear-gradient(-45deg,
		transparent,
		transparent calc(3px * 1.414),
		var(--diffs-bg-buffer) calc(3px * 1.414),
		var(--diffs-bg-buffer) calc(4px * 1.414));
	background-size: 8px 8px;
	background-position: 5px 0;
	background-origin: border-box;
}
`;

// Files panel review only (WorkspaceReviewPane): filler rows — the empty side
// of a split hunk and the row an inline feedback composer opens in — keep their
// content treatment, but their line-number gutter sits on the canvas instead of
// painting a grey block. Not part of the shared surface CSS, so the center diff
// tab, file view, and cloud diffs are unchanged.
export const AO_PIERRE_FILES_REVIEW_CSS = `
/* Inset the line-number column so numbers sit in from the panel edge instead
   of lining up under the file header's chevron: a wider right-aligned column
   plus a little more leading space. */
:host {
	--diffs-min-number-column-width: 4ch;
}

[data-column-number][data-column-number],
[data-gutter-buffer][data-gutter-buffer] {
	padding-left: 3ch;
}

[data-gutter-buffer="buffer"][data-gutter-buffer] {
	--diffs-line-bg: var(--diffs-bg);
}

[data-gutter-buffer="annotation"][data-gutter-buffer] {
	--diffs-annotation-bg: var(--diffs-bg);
}
`;
