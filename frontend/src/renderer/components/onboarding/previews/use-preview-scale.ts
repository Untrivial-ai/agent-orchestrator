"use client";

import { useRef } from "react";
import type { CSSProperties } from "react";

export function usePreviewScale(baseWidth: number, baseHeight: number) {
	const viewportRef = useRef<HTMLDivElement>(null);

	return {
		viewportRef,
		viewportStyle: {
			maxWidth: `${baseWidth}px`,
			aspectRatio: `${baseWidth} / ${baseHeight}`,
			containerType: "inline-size",
		} as CSSProperties,
		canvasStyle: {
			width: `${baseWidth}px`,
			height: `${baseHeight}px`,
			"--preview-scale": `min(1, calc(100cqw / ${baseWidth}px))`,
			transform: "scale(var(--preview-scale))",
			transformOrigin: "top left",
		} as CSSProperties,
	};
}
