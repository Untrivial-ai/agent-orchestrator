import { memo, useEffect, useRef } from "react";
import QRCodeStyling from "qr-code-styling";
import aoLogo from "../../assets/ao-logo.svg";

/**
 * Rounded-dot QR (qr-code-styling) with the AO logo in the middle, always drawn
 * as dark modules on a light card.
 *
 * The polarity is a correctness constraint, not a style choice. This used to
 * take its colour from `--color-text-settings-title` over a transparent
 * background, which in the dark theme produced light modules on dark — an
 * inverted code. Apple's AVFoundation and Google Lens both decode inverted
 * codes; the bundled ML Kit scanner that expo-camera uses on Android does not.
 * So the pairing QR scanned everywhere *except* our own Android app, which is
 * exactly the shape the bug reports took.
 *
 * Confirmed by a single-variable scan sweep on a device that reproduced it:
 * with module size, logo size and finder shape each held constant, every
 * inverted variant failed and every dark-on-light variant passed — including
 * one that kept the full-size logo and the round finders. Polarity was the
 * only factor that predicted the outcome.
 *
 * Keep the background opaque. A transparent background inherits whatever sits
 * behind it, which is how the inversion got in.
 */
const INK = "#111113";
const PAPER = "#ffffff";

/**
 * Quiet zone the spec requires, in modules.
 *
 * Measured in modules rather than pixels or a fraction of the rendered size,
 * because that is the unit a scanner reads it in. A short payload produces
 * fewer, larger modules, so a fixed percentage that looks right on the long
 * pairing code silently leaves the small store codes under a third of the
 * clearance they need.
 */
const QUIET_ZONE_MODULES = 4;

/** Margin in px that yields QUIET_ZONE_MODULES around a code of `modules`
 *  modules drawn at `size` px, given the margin eats into that same size:
 *  margin / ((size - 2·margin) / modules) = QUIET_ZONE_MODULES. */
function quietZonePx(size: number, modules: number): number {
	return Math.ceil((QUIET_ZONE_MODULES * size) / (modules + 2 * QUIET_ZONE_MODULES));
}

export const StyledQRCode = memo(function StyledQRCode({
	value,
	size,
	showLogo = true,
	className,
	"data-qr-value": dataQrValue,
}: {
	value: string;
	size: number;
	showLogo?: boolean;
	className?: string;
	"data-qr-value"?: string;
}) {
	const ref = useRef<HTMLDivElement>(null);

	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		try {
			const qr = new QRCodeStyling({
				width: size,
				height: size,
				type: "svg",
				data: value,
				image: showLogo ? aoLogo : undefined,
				// Replaced below once the module count is known.
				margin: 0,
				// Lower error correction = fewer modules = chunkier dots, so a
				// generated (long-payload) code reads at the same visual weight as
				// the short placeholder behind the blur.
				qrOptions: { errorCorrectionLevel: "M" },
				backgroundOptions: { color: PAPER },
				dotsOptions: { color: INK, type: "rounded" },
				cornersSquareOptions: { color: INK, type: "extra-rounded" },
				cornersDotOptions: { color: INK, type: "dot" },
				imageOptions: { imageSize: 0.45, margin: 8, hideBackgroundDots: true, crossOrigin: "anonymous" },
			});
			// The module count is only known once the payload has been encoded, so
			// the quiet zone is applied as a second pass rather than guessed from
			// the rendered size.
			const modules = qr._qr?.getModuleCount() ?? 0;
			if (modules > 0) qr.update({ margin: quietZonePx(size, modules) });
			el.replaceChildren();
			qr.append(el);
		} catch {
			// jsdom / non-browser environments can't render the SVG; the QR is
			// purely visual, so fail silent rather than crash the settings page.
		}
	}, [value, size, showLogo]);

	return <div ref={ref} className={className} data-qr-value={dataQrValue} />;
});
