"use client";

import Image from "next/image";

// Apple's and Google's own badge artwork, served from public/badges. Both
// companies' identity guidelines require the supplied asset — recreating the
// lockup in our own type, which is what this component used to do, is a
// violation of each, and loses the recognition that makes a badge worth using
// in the first place. Downloaded from Apple's Marketing Tools toolbox and the
// Google Play badge page; permitted solely to link to the store listing, which
// is what both callers do.
//
// The wording differs between them ("Download on the" vs "Get it on") because
// each guideline fixes its own. That mismatch is universal and correct — do not
// "harmonize" it.
const STORE = {
  ios: {
    src: "/badges/app-store.svg",
    alt: "Download on the App Store",
    // Apple's badge is trimmed to the artwork: 119.66 x 40.
    width: 120,
    height: 40,
    className: "h-10 w-auto",
  },
  android: {
    src: "/badges/google-play.png",
    alt: "Get it on Google Play",
    // Google's ships with its mandated clear space baked in (646 x 250), so it
    // needs ~1.45x Apple's height for the two wordmarks to sit at the same
    // optical size. Do not crop the padding out to make the numbers match.
    width: 150,
    height: 58,
    className: "h-[3.625rem] w-auto",
  },
} as const;

export type StoreKey = keyof typeof STORE;

function BadgeImage({ store }: { store: StoreKey }) {
  const { src, alt, width, height, className } = STORE[store];
  return (
    <Image
      src={src}
      alt={alt}
      width={width}
      height={height}
      // unoptimized keeps the vector crisp and skips a pointless transform on a
      // 5 KB asset.
      unoptimized
      className={className}
    />
  );
}

const WRAP_CLASS =
  "inline-flex shrink-0 items-center rounded-xl transition-opacity hover:opacity-85 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background motion-reduce:transition-none";

export function StoreBadgeLink({
  store,
  href,
  onClick,
}: {
  store: StoreKey;
  href: string;
  onClick?: () => void;
}) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      onClick={onClick}
      aria-label={STORE[store].alt}
      className={WRAP_CLASS}
    >
      <BadgeImage store={store} />
    </a>
  );
}

export function StoreBadgeButton({
  store,
  onClick,
}: {
  store: StoreKey;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={STORE[store].alt}
      className={WRAP_CLASS}
    >
      <BadgeImage store={store} />
    </button>
  );
}
