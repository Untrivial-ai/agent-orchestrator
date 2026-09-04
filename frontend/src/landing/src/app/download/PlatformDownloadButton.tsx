"use client";

import { COMPANY } from "@ao/shared/constants";
import { FaApple, FaLinux, FaWindows } from "react-icons/fa";
import { isMacPlatform, Platform, usePlatform } from "../hooks/useOS";

type DownloadTarget = {
  href: string;
  label: string;
  icon: "apple" | "windows" | "linux";
};

export interface PlatformDownloadLinks {
  linux?: string;
  macArm64?: string;
  macX64?: string;
  windows?: string;
}

function getDownloadTarget(
  platform: Platform,
  downloads: PlatformDownloadLinks,
): DownloadTarget | null {
  if (platform === Platform.Windows) {
    return downloads.windows
      ? {
          href: downloads.windows,
          label: "Download for Windows",
          icon: "windows",
        }
      : null;
  }

  if (platform === Platform.Linux) {
    return downloads.linux
      ? {
          href: downloads.linux,
          label: "Download for Linux",
          icon: "linux",
        }
      : null;
  }

  if (platform === Platform.MacIntel) {
    return downloads.macX64
      ? {
          href: downloads.macX64,
          label: "Download for macOS (Intel)",
          icon: "apple",
        }
      : null;
  }

  if (platform === Platform.Mobile) {
    return {
      href: `${COMPANY.DOCS_URL}/installation/`,
      label: "Open install guide",
      icon: "apple",
    };
  }

  if (isMacPlatform(platform)) {
    return downloads.macArm64
      ? {
          href: downloads.macArm64,
          label: "Download for macOS",
          icon: "apple",
        }
      : null;
  }

  return downloads.macArm64
    ? {
        href: downloads.macArm64,
        label: "Download for macOS",
        icon: "apple",
      }
    : null;
}

export function PlatformDownloadButton({
  downloads,
}: {
  downloads: PlatformDownloadLinks;
}) {
  const { platform } = usePlatform();
  const target = getDownloadTarget(platform, downloads);
  if (target === null) return null;
  const Icon =
    target.icon === "windows"
      ? FaWindows
      : target.icon === "linux"
        ? FaLinux
        : FaApple;

  return (
    <a
      href={target.href}
      className="inline-flex shrink-0 items-center gap-2 whitespace-nowrap rounded-3xl bg-foreground px-3 py-2 text-sm font-semibold tracking-[-0.5px] text-background transition-opacity hover:opacity-90 sm:px-6 sm:py-3 sm:text-base"
    >
      <Icon className="size-4 shrink-0" aria-hidden="true" />
      {target.label}
    </a>
  );
}
