"use client";

import { COMPANY } from "@ao/shared/constants";
import { ArrowUpRight } from "lucide-react";
import { usePathname } from "next/navigation";
import { HashLink } from "../HashLink/HashLink";
import { track } from "@/lib/analytics";
import { TileWordmark } from "./TileWordmark";

/**
 * Coarse destination for an outbound link.
 *
 * A category rather than the URL: this answers "how many people go to Discord
 * versus GitHub" without recording exactly which page, and a new link is covered
 * automatically instead of needing to be tagged by hand.
 */
function outboundDestination(href: string): string {
  try {
    const host = new URL(href).hostname.replace(/^www\./, "");
    if (host.endsWith("github.com")) return href.includes("/releases") ? "github_releases" : "github";
    if (host.endsWith("discord.com") || host.endsWith("discord.gg")) return "discord";
    if (host === "x.com" || host.endsWith("twitter.com")) return "x";
    if (host.endsWith("linkedin.com")) return "linkedin";
    return host;
  } catch {
    return "unknown";
  }
}

export function Footer() {
  const pathname = usePathname();
  if (pathname === "/download") return null;
  // Docs pages are full-height with their own sidebar/TOC — no marketing footer.
  if (pathname === "/docs" || pathname.startsWith("/docs/")) return null;

  return (
    <footer className="bg-card">
      <div className="px-4 sm:px-8 lg:px-[30px]">
        <div className="max-w-7xl mx-auto py-14 sm:py-20">
          <div className="grid gap-10 lg:grid-cols-[minmax(0,1fr)_minmax(0,0.75fr)] lg:items-start">
          <div className="flex flex-col gap-5">
            <div className="text-left text-4xl font-semibold leading-[1.08] tracking-[-0.04em] text-foreground sm:hidden">
              <p>Spawn Agents</p>
              <p>Step Away</p>
              <p>Ship Faster</p>
            </div>
            <div className="hidden sm:block">
              <TileWordmark />
            </div>
          </div>

          <div className="grid grid-cols-2 gap-6 sm:grid-cols-3 sm:gap-8">
            <FooterColumn
              title="Product"
              links={[
                { href: "/#features", label: "Features" },
                { href: "/#agents", label: "Agents" },
                { href: `${COMPANY.DOCS_URL}/installation/`, label: "Install", external: true },
                { href: `${COMPANY.DOCS_URL}/cli/`, label: "CLI", external: true },
                { href: "/changelog", label: "Changelog" },
                { href: "/design-partners", label: "Design Partners" },
              ]}
            />

            <FooterColumn
              title="Docs"
              links={[
                { href: `${COMPANY.DOCS_URL}/`, label: "Overview", external: true },
                { href: `${COMPANY.DOCS_URL}/architecture/`, label: "Architecture", external: true },
                { href: `${COMPANY.DOCS_URL}/plugins/`, label: "Plugins", external: true },
                { href: `${COMPANY.GITHUB_URL}/releases`, label: "Releases", external: true },
                { href: "/privacy/", label: "Privacy" },
              ]}
            />

            <div className="col-span-2 sm:col-span-1">
              <FooterColumn
                title="Community"
                links={[
                  { href: COMPANY.GITHUB_URL, label: "GitHub", external: true },
                  { href: COMPANY.DISCORD_URL, label: "Discord", external: true },
                  { href: COMPANY.LINKEDIN_URL, label: "LinkedIn", external: true },
                  { href: COMPANY.X_URL, label: "X", external: true },
                ]}
              />
            </div>
          </div>
          </div>
        </div>
      </div>
    </footer>
  );
}

interface FooterLink {
  href: string;
  label: string;
  external?: boolean;
}

const FOOTER_ROW_COUNT = 6;

function FooterColumn({
  title,
  links,
}: {
  title: string;
  links: FooterLink[];
}) {
  return (
    <div className="min-w-0">
      <p className="pb-2 text-sm font-medium text-foreground">
        {title}
      </p>
      <ul className="space-y-1">
        {Array.from({ length: FOOTER_ROW_COUNT }).map((_, index) => {
          const link = links[index];

          return (
          <li key={link?.href ?? `${title}-empty-${index}`}>
            {!link ? (
              <div className="hidden min-h-8 sm:block" aria-hidden="true" />
            ) : link.external ? (
              <a
                href={link.href}
                target="_blank"
                rel="noopener noreferrer"
                className="group flex min-h-8 items-center justify-between gap-1 py-1.5 text-[13px] text-muted-foreground transition-colors hover:text-foreground sm:gap-3 sm:text-sm"
                onClick={() =>
                  track("outbound_link_clicked", {
                    destination: outboundDestination(link.href),
                    label: link.label,
                  })
                }
              >
                {link.label}
                <ArrowUpRight className="hidden h-3 w-3 shrink-0 opacity-0 transition-opacity group-hover:opacity-100 sm:block" />
              </a>
            ) : (
              <HashLink
                href={link.href}
                className="flex min-h-8 items-center justify-between gap-1 py-1.5 text-[13px] text-muted-foreground transition-colors hover:text-foreground sm:gap-3 sm:text-sm"
              >
                {link.label}
              </HashLink>
            )}
          </li>
          );
        })}
      </ul>
    </div>
  );
}
