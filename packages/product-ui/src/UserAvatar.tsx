import { useState } from "react";
import { cn } from "./utils";

export type UserAvatarProps = {
	name: string;
	imageUrl?: string;
	className?: string;
};

function initials(name: string): string {
	return name
		.replace(/^@/, "")
		.trim()
		.split(/[-_\s]+/)
		.filter(Boolean)
		.slice(0, 2)
		.map((part) => part[0]?.toUpperCase() ?? "")
		.join("") || "?";
}

/** A provider-neutral avatar that keeps its initials visible until the image is usable. */
export function UserAvatar({ name, imageUrl, className }: UserAvatarProps) {
	const normalizedName = name.replace(/^@/, "").trim();
	const normalizedImageUrl = imageUrl?.trim() ?? "";
	const [loadedUrl, setLoadedUrl] = useState<string>();
	const [failedUrl, setFailedUrl] = useState<string>();
	const loaded = loadedUrl === normalizedImageUrl && failedUrl !== normalizedImageUrl;
	const shouldLoad = normalizedImageUrl !== "" && failedUrl !== normalizedImageUrl;

	return (
		<span
			aria-hidden="true"
			className={cn("relative inline-flex size-icon-sm shrink-0 items-center justify-center overflow-hidden rounded-full bg-muted text-micro font-semibold text-muted-foreground", className)}
		>
			{loaded ? null : initials(normalizedName)}
			{shouldLoad ? (
				<img
					alt=""
					className={cn("absolute inset-0 size-full object-cover", loaded ? "opacity-100" : "opacity-0")}
					draggable={false}
					loading="lazy"
					onError={() => setFailedUrl(normalizedImageUrl)}
					onLoad={() => setLoadedUrl(normalizedImageUrl)}
					referrerPolicy="no-referrer"
					src={normalizedImageUrl}
				/>
			) : null}
		</span>
	);
}
