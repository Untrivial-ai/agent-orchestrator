"use client";

import { IOS_APP_STORE_URL } from "@ao/shared/constants";
import { useState } from "react";
import { track } from "../../lib/analytics";
import { usePlatform } from "../hooks/useOS";
import { StoreBadgeButton, StoreBadgeLink } from "./StoreBadge";
import { StoreQRDialog } from "./StoreQRDialog";

// Picks the install path by device. On the phone the badge is the install —
// one tap into the listing. On a desktop the same badge opens a QR, because
// the app cannot install on the machine the visitor is reading this on.
export function MobileAppCTA() {
  const { mobileOS } = usePlatform();
  const [open, setOpen] = useState(false);

  if (mobileOS === "ios") {
    return (
      <StoreBadgeLink
        store="ios"
        href={IOS_APP_STORE_URL}
        onClick={() => track("app_store_clicked", { surface: "badge" })}
      />
    );
  }

  return (
    <>
      <StoreBadgeButton
        store="ios"
        onClick={() => {
          track("store_qr_opened", { platform: "ios" });
          setOpen(true);
        }}
      />
      <StoreQRDialog
        platform="ios"
        url={IOS_APP_STORE_URL}
        open={open}
        onClose={() => setOpen(false)}
      />
    </>
  );
}
