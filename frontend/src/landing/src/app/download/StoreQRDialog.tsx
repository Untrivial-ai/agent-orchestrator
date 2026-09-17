"use client";

import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { ExternalLink, X } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { useCallback, useEffect } from "react";
import { track } from "../../lib/analytics";

export type StorePlatform = "ios" | "android";

// One dialog for both stores. Before launch these were two very different
// flows — a TestFlight invite and a Google tester-group opt-in — and each
// needed its own numbered walkthrough. Both are now a single tap on a public
// listing, so the only thing left to solve is the handoff: the visitor is on a
// desktop and the app installs on the phone in their pocket. That is what the
// QR is for, and it is the whole dialog.
const COPY: Record<
  StorePlatform,
  { title: string; body: string; action: string; event: string }
> = {
  ios: {
    title: "Get AO Mobile on iPhone",
    body: "Scan with your phone's camera to open the App Store.",
    action: "Open the App Store",
    event: "app_store_clicked",
  },
  android: {
    title: "Get AO Mobile on Android",
    body: "Scan with your phone's camera to open Google Play.",
    action: "Open Google Play",
    event: "play_store_clicked",
  },
};

interface StoreQRDialogProps {
  platform: StorePlatform;
  url: string;
  open: boolean;
  onClose: () => void;
}

export function StoreQRDialog({
  platform,
  url,
  open,
  onClose,
}: StoreQRDialogProps) {
  const shouldReduceMotion = useReducedMotion();
  const close = useCallback(() => onClose(), [onClose]);
  const copy = COPY[platform];

  useEffect(() => {
    if (!open) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") close();
    };
    document.addEventListener("keydown", onKey);
    // The page behind a modal must not scroll under it.
    const { overflow } = document.body.style;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = overflow;
    };
  }, [open, close]);

  return (
    <AnimatePresence>
      {open ? (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
          <motion.button
            type="button"
            tabIndex={-1}
            aria-label="Close"
            onClick={close}
            initial={shouldReduceMotion ? false : { opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: shouldReduceMotion ? 0 : 0.18 }}
            className="absolute inset-0 cursor-default bg-black/70 backdrop-blur-sm outline-none"
          />

          <motion.div
            role="dialog"
            aria-modal="true"
            aria-labelledby={`store-dialog-title-${platform}`}
            initial={
              shouldReduceMotion ? false : { opacity: 0, y: 12, scale: 0.98 }
            }
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: 8, scale: 0.98 }}
            transition={
              shouldReduceMotion
                ? { duration: 0 }
                : { type: "spring", duration: 0.34, bounce: 0.12 }
            }
            className="relative max-h-[calc(100dvh-2rem)] w-full max-w-sm overflow-y-auto rounded-2xl border border-border bg-card p-6 text-center shadow-[0_32px_80px_-24px_rgba(0,0,0,0.8)] sm:p-7"
          >
            <button
              type="button"
              onClick={close}
              aria-label="Close"
              autoFocus
              className="absolute right-2 top-2 grid size-10 place-items-center rounded-full text-muted-foreground transition-colors hover:bg-foreground/10 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring motion-reduce:transition-none"
            >
              <X className="size-4" aria-hidden="true" />
            </button>

            <h2
              id={`store-dialog-title-${platform}`}
              className="px-6 text-xl font-semibold text-foreground"
            >
              {copy.title}
            </h2>
            <p className="mt-2 text-sm leading-6 text-muted-foreground">
              {copy.body}
            </p>

            <div className="mt-5 flex justify-center">
              {/* White plate, always — a QR needs the light quiet zone to scan,
                  whatever theme the page is in. */}
              <div className="rounded-xl bg-white p-3">
                <QRCodeSVG value={url} size={172} className="block" />
              </div>
            </div>

            <a
              href={url}
              target="_blank"
              rel="noreferrer"
              onClick={() => track(copy.event, { surface: "qr_dialog" })}
              className="mt-5 flex w-full items-center justify-center gap-2 rounded-3xl border border-border px-4 py-3 text-sm font-semibold text-foreground transition-colors hover:bg-foreground/5"
            >
              {copy.action}
              <ExternalLink className="size-3.5" aria-hidden="true" />
            </a>

            <p className="mt-3 text-xs leading-5 text-muted-foreground">
              Free, and pairs with the desktop app in a couple of taps.
            </p>
          </motion.div>
        </div>
      ) : null}
    </AnimatePresence>
  );
}
