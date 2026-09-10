/** Only normalized colors cross the main/preload boundary. */
export interface OmarchyPalette {
	background: string;
	surface?: string;
	sidebar?: string;
	foreground: string;
	accent: string;
	selection: string;
	cursor: string;
	ansi: string[];
}

export function luminance(hex: string): number {
	const rgb = [1, 3, 5].map((offset) => {
		const c = Number.parseInt(hex.slice(offset, offset + 2), 16) / 255;
		return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
	});
	return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
}

export function contrast(a: string, b: string): number {
	const x = luminance(a), y = luminance(b);
	return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

export function readableText(background: string, preferred: string): string {
	if (contrast(background, preferred) >= 4.5) return preferred;
	return contrast(background, "#ffffff") > contrast(background, "#000000") ? "#ffffff" : "#000000";
}
