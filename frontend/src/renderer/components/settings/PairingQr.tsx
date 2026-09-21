import { memo, useEffect, useMemo, useState } from "react";
import { Loader2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { StyledQRCode } from "./StyledQRCode";
import { scramblePairingCodes } from "./qrScramble";

/** Decoy layers cycled while waiting.
 *
 * Four, phase-offset by a quarter cycle each, so exactly two are on screen at
 * any moment — one rising, one falling. That overlap is what makes the field
 * appear to move: with layers fading fully out before the next arrived, it
 * read as a slideshow of separate codes rather than as one code churning. */
const SCRAMBLE_LAYERS = 4;

/** Keep the finished code ready to scan quickly; it only dissolves in. */
export const QR_RESOLVE_MS = 260;

/** One pass through the decoy stack. Unhurried on purpose: a fast shuffle
 *  reads as a glitch, a slow one as something being worked out.
 *
 *  MUST match the ao-qr-decoy animation duration in styles.css — the per-layer
 *  delays below are phase offsets within this cycle, and they only produce the
 *  intended overlap if both agree. */
export const QR_SCRAMBLE_CYCLE_MS = 4800;

/**
 * The pairing code, and the wait that precedes it, as one continuous object.
 *
 * Bringing the tunnel up takes ~30s. Rather than show a message and then
 * replace it with a QR, this renders decoy codes of the same shape — same QR
 * version, same module size, same corner markers, same logo — shuffling
 * underneath, and dissolves them into the real code when it arrives. Only the
 * data changes; the object on screen never does.
 *
 * The logo sits still throughout, so there is a fixed point to look at while
 * the field around it churns.
 *
 * Owns its own square box and puts the caption beneath it. The caption cannot
 * live inside the clipping aspect-square the panel used to impose — that is
 * what cut it in half and pushed it under the button below.
 */
export const PairingQr = memo(function PairingQr({
	value,
	size,
	caption,
	className,
}: {
	/** The real pairing code, or null while it is still being prepared. */
	value: string | null;
	size: number;
	caption: string;
	className?: string;
}) {
	const decoys = useMemo(() => scramblePairingCodes(SCRAMBLE_LAYERS), []);
	// Kept mounted through the crossfade so the two codes overlap rather than
	// one being swapped for the other.
	const [scrambling, setScrambling] = useState(!value);

	useEffect(() => {
		if (!value) {
			setScrambling(true);
			return;
		}
		const timer = setTimeout(() => setScrambling(false), QR_RESOLVE_MS);
		return () => clearTimeout(timer);
	}, [value]);

	const resolved = Boolean(value);

	return (
		<div className={cn("flex w-full flex-col", className)}>
			{/* Light card. The code is drawn dark-on-light because Android's
			    scanner will not decode an inverted one.

			    The code fills the card: StyledQRCode draws its own quiet zone, so
			    insetting it here only stacked a second margin on top of that one
			    and shrank the modules for nothing. */}
			<div className="relative aspect-square w-full overflow-hidden rounded-md bg-white">
				{/* Drifts only while waiting: a code being scanned must hold still. */}
				<div className={cn("absolute inset-0", !resolved && "ao-qr-drift")}>
					{scrambling &&
						decoys.map((code, i) => (
							<div
								key={code}
								className={cn("ao-qr-decoy absolute inset-0", resolved && "ao-qr-decoy--settling")}
								style={
									resolved
										? undefined
										: { animationDelay: `${i * (QR_SCRAMBLE_CYCLE_MS / SCRAMBLE_LAYERS)}ms` }
								}
								aria-hidden="true"
							>
								<StyledQRCode value={code} size={size} className="ao-qr-visual block size-full [&_svg]:size-full" />
							</div>
						))}
					{value && (
						<div
							key={value}
							className="ao-qr-resolved absolute inset-0"
							style={{ animationDuration: `${QR_RESOLVE_MS}ms` }}
						>
							<StyledQRCode
								value={value}
								data-qr-value={value}
								size={size}
											className="ao-qr-visual block size-full [&_svg]:size-full"
							/>
						</div>
					)}
				</div>
			</div>
			{/* Fixed height, not min-height: the button below must not move when
			    the caption appears or goes. Clamped rather than grown, so a long
			    translation wrapping to a second line still cannot shift it. */}
			<div className="flex h-6 items-center justify-center px-2">
				{!resolved && (
					<p
						className="flex items-center justify-center gap-2 text-center text-caption leading-(--leading-settings-mobile-hint) text-settings-muted"
						role="status"
					>
						<Loader2 className="size-3.5 shrink-0 animate-spin" aria-hidden="true" />
						<span className="line-clamp-2">{caption}</span>
					</p>
				)}
			</div>
		</div>
	);
});
