"use client";

import { ANDROID_PLAY_STORE_URL } from "@ao/shared/constants";
import { useState } from "react";
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
      <StoreBadgeLink store="android" href={ANDROID_PLAY_STORE_URL} />
    );
  }

  return (
    <>
      <StoreBadgeButton store="android" onClick={() => setOpen(true)} />
      <StoreQRDialog
        platform="android"
        url={ANDROID_PLAY_STORE_URL}
        open={open}
        onClose={() => setOpen(false)}
      />
    </>
  );
}
