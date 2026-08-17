"use client";

import {
  COMPANY,
  HERO_SUBHEADLINE,
  TAGLINE,
} from "@ao/shared/constants";
import { Star } from "lucide-react";
import { useState } from "react";
import { FaGithub } from "react-icons/fa";
import { track } from "@/lib/analytics";
import { DownloadButton } from "../DownloadButton";
import { ProductDemo } from "./components/ProductDemo";

const INSTALL_COMMAND = "brew install agentwrapper/tap/agent-orchestrator";
// Wraps at the path separators instead of mid-word once the pill goes two-line.
const INSTALL_COMMAND_PARTS = INSTALL_COMMAND.split("/");

function formatStarCount(count: number) {
  if (count >= 1000) {
    return `${(count / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  }

  return count.toString();
}

interface HeroSectionProps {
  initialStars: number | null;
}

export function HeroSection({ initialStars }: HeroSectionProps) {
  const [copiedCommand, setCopiedCommand] = useState(false);
  const stars = initialStars;

  const githubButtonLabel =
    stars === null
      ? "Stars on GitHub"
      : `${formatStarCount(stars)} Stars on GitHub`;

  const copyInstallCommand = async () => {
    if (!navigator.clipboard) return;

    await navigator.clipboard.writeText(INSTALL_COMMAND);
    // A copy is download intent that never touches a download button, so without
    // this the brew path is invisible in the acquisition funnel.
    track("install_command_copied", { method: "brew" });
    setCopiedCommand(true);
    window.setTimeout(() => setCopiedCommand(false), 1600);
  };

  return (
    <div className="relative">
      <div className="relative flex flex-col items-center overflow-hidden pt-24 pb-8 sm:pt-32 sm:pb-10 lg:pt-36 lg:pb-12">
        <div className="relative w-full max-w-[1600px] mx-auto px-4 sm:px-8 lg:px-[30px]">
          <div className="flex flex-col items-center text-center">
            <div className="space-y-5 sm:space-y-7 select-none">
              <h1 className="font-sans text-4xl sm:text-5xl md:text-6xl lg:text-[4.75rem] font-normal tracking-[-0.5px] leading-[0.98] text-foreground max-w-6xl mx-auto text-balance">
                {TAGLINE}
              </h1>
              <p
                id="hero-subheadline"
                className="text-base sm:text-xl font-normal leading-8 text-muted-foreground max-w-4xl mx-auto text-balance"
              >
                {HERO_SUBHEADLINE}
              </p>
            </div>

            <div className="flex flex-wrap items-center justify-center gap-2 sm:gap-4 mt-6 sm:mt-8">
              <DownloadButton className="rounded-3xl" />
              <a
                href={COMPANY.GITHUB_URL}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={githubButtonLabel}
                className="inline-flex items-center gap-2.5 rounded-3xl border border-border bg-background px-5 py-2.5 sm:py-3 text-sm sm:text-base font-normal tracking-[-0.5px] text-foreground transition-colors hover:bg-muted"
              >
                <FaGithub className="size-4" aria-hidden="true" />
                <span>Star on GitHub</span>
                {stars !== null ? (
                  <span className="flex items-center gap-1 pl-0.5 text-muted-foreground">
                    <Star
                      className="size-3.5 fill-yellow-400 text-yellow-400"
                      aria-hidden="true"
                    />
                    <span className="tabular-nums">
                      {formatStarCount(stars)}
                    </span>
                  </span>
                ) : null}
              </a>
            </div>

            <div className="landing-install-command mt-4">
              <button
                type="button"
                aria-label={`Copy brew install command: ${INSTALL_COMMAND}`}
                title="Click to copy"
                className="group flex min-h-11 w-full max-w-xl items-start gap-2 rounded-3xl border border-border bg-card/70 px-3 py-2.5 text-left font-mono text-xs tracking-[0.5px] text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground sm:w-auto sm:items-center sm:overflow-hidden sm:text-sm"
                onClick={copyInstallCommand}
              >
                <span className="text-foreground/40" aria-hidden="true">
                  $
                </span>
                <code className="min-w-0 flex-1 break-words whitespace-normal text-foreground/80 sm:flex-none sm:truncate sm:whitespace-nowrap">
                  {INSTALL_COMMAND_PARTS.map((part, index) => (
                    <span key={part}>
                      {index > 0 ? "/" : null}
                      {part}
                      {index < INSTALL_COMMAND_PARTS.length - 1 ? <wbr /> : null}
                    </span>
                  ))}
                </code>
                <span
                  className="ml-2 inline-flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground transition-colors group-hover:text-foreground"
                  aria-hidden="true"
                >
                  <svg
                    className="h-3.5 w-3.5"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.8"
                  >
                    <rect x="9" y="9" width="12" height="12" rx="2" />
                    <path d="M5 15V5a2 2 0 0 1 2-2h10" />
                  </svg>
                  {copiedCommand ? "Copied" : "Copy"}
                </span>
              </button>
            </div>
          </div>

          <div className="relative w-full max-w-7xl mx-auto mt-12 sm:mt-16 lg:mt-20">
            <ProductDemo />
          </div>
        </div>
      </div>
      <div
        className="pointer-events-none absolute inset-x-0 bottom-0 z-10 h-[100px]"
        style={{
          background:
            "linear-gradient(to bottom, rgba(0,0,0,0) 0%, var(--background) 100%)",
        }}
      />
    </div>
  );
}
