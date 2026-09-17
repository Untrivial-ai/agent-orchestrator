"use client";

import { ANDROID_PLAY_STORE_URL } from "@ao/shared/constants";
import { useState } from "react";
import { track } from "../../lib/analytics";
import { usePlatform } from "../hooks/useOS";
import { StoreBadgeButton, StoreBadgeLink } from "./StoreBadge";
import { StoreQRDialog } from "./StoreQRDialog";

// Mirrors MobileAppCTA: direct link on the device that can install, QR handoff
// everywhere else. Keyed off mobileOS rather than Platform.Mobile so an
// Android visitor does not get an iOS QR and vice versa.
export function AndroidAppCTA() {
  const { mobileOS } = usePlatform();
  const [open, setOpen] = useState(false);

  if (mobileOS === "android") {
    return (
      <StoreBadgeLink
        store="android"
        href={ANDROID_PLAY_STORE_URL}
        onClick={() => track("play_store_clicked", { surface: "badge" })}
      />
    );
  }

  return (
    <>
      <StoreBadgeButton
        store="android"
        onClick={() => {
          track("store_qr_opened", { platform: "android" });
          setOpen(true);
        }}
      />
      <StoreQRDialog
        platform="android"
        url={ANDROID_PLAY_STORE_URL}
        open={open}
        onClose={() => setOpen(false)}
      />
    </>
  );
}
