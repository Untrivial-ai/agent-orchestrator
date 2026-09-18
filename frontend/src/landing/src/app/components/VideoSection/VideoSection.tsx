"use client";

import Image from "next/image";
import dynamic from "next/dynamic";
import { useState } from "react";

// Loaded on demand, not with the page. The player is ~1.1MB and the section is
// below the fold behind a click, so a static import put it on the critical path
// of every homepage visit for a video most visitors never play. It is only
// rendered once `playing` is true, so there is nothing to show until then.
const MuxPlayer = dynamic(() => import("@mux/mux-player-react"), { ssr: false });

const MUX_PLAYBACK_ID =
	process.env.NEXT_PUBLIC_MUX_PLAYBACK_ID ??
	"cpmHxjRygocH1rPeKq6jk4UYxGghl8B8ABcop4Gc01b8";
const VIDEO_TITLE = "AO Demo";

function PlayIcon({ className = "" }: { className?: string }) {
	return (
		<svg
			className={className}
			viewBox="0 0 24 24"
			fill="currentColor"
			aria-hidden="true"
		>
			<path d="M8 5.5v13a1 1 0 0 0 1.53.85l10.2-6.5a1 1 0 0 0 0-1.7L9.53 4.65A1 1 0 0 0 8 5.5Z" />
		</svg>
	);
}

export function VideoSection() {
	const [playing, setPlaying] = useState(false);

	return (
		<section id="see-it" className="relative px-4 py-16 sm:px-8 sm:py-20 lg:px-[30px] lg:py-24">
			<div className="mx-auto max-w-7xl">
				<div className="max-w-3xl text-left select-none">
					<h2 className="text-2xl sm:text-3xl lg:text-4xl font-semibold text-foreground">
						See it in action
					</h2>
					<p className="mt-3 text-base text-muted-foreground">
						Watch AO run a fleet of agents end to end on a single repo, from task to merged PR.
					</p>
				</div>

				<div className="relative mx-auto mt-12 w-full">
					<div
						data-testid="video-frame"
						className="relative aspect-video overflow-hidden bg-black"
					>
						{playing ? (
							<MuxPlayer
								playbackId={MUX_PLAYBACK_ID}
								autoPlay
								metadata={{ video_title: VIDEO_TITLE }}
								title={VIDEO_TITLE}
								className="absolute inset-0 h-full w-full"
							/>
						) : (
							<button
								type="button"
								onClick={() => setPlaying(true)}
								aria-label={`Play video: ${VIDEO_TITLE}`}
								className="group absolute inset-0 cursor-pointer"
							>
								<Image
									src="/mux-video-preview.jpg"
									alt="Still from the Agent Orchestrator demo video"
									fill
									sizes="(min-width: 1280px) 1280px, 100vw"
									className="object-cover"
								/>
								<span className="absolute inset-0 grid place-items-center">
									<PlayIcon className="h-14 w-14 translate-x-[3px] text-white transition-transform duration-200 group-hover:scale-110 sm:h-16 sm:w-16" />
								</span>
							</button>
						)}
					</div>
				</div>
			</div>
		</section>
	);
}
