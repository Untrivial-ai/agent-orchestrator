"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { MobileNav } from "./components/MobileNav";
import { DesktopNav } from "./components/DesktopNav";
import { AOLogo } from "./components/AOLogo";

interface HeaderProps {
  ctaButtons: React.ReactNode;
}

export function Header({ ctaButtons }: HeaderProps) {
  const pathname = usePathname();
  const hasTransparentHero =
    pathname === "/" || pathname === "/design-partners";
  const [hasScrolled, setHasScrolled] = useState(false);

  useEffect(() => {
    if (!hasTransparentHero) {
      setHasScrolled(false);
      return;
    }

    const updateScrollState = () => {
      setHasScrolled(window.scrollY > 0);
    };

    updateScrollState();
    window.addEventListener("scroll", updateScrollState, { passive: true });
    return () => window.removeEventListener("scroll", updateScrollState);
  }, [hasTransparentHero]);

  // Placed after the hooks above so hook order stays stable (Rules of Hooks):
  // an early return before useState/useEffect breaks the client component, and
  // its scroll listener never attaches — leaving the hero header stuck
  // transparent instead of turning solid on scroll.
  if (pathname === "/download") return null;

  return (
    <header
      className={
        hasTransparentHero
          ? `fixed inset-x-0 top-0 z-50 transition-colors ${
              hasScrolled ? "bg-background" : "bg-transparent"
            }`
          : "sticky top-0 z-50 bg-background"
      }
    >
      <div className="px-4 sm:px-8 lg:px-[30px]">
        <div className="max-w-7xl mx-auto">
          <div className="flex items-center justify-between h-14">
            <div className="flex items-center">
              <Link
                href="/"
                className="flex items-center gap-2 text-foreground hover:text-foreground/80 transition-colors"
              >
                <AOLogo />
              </Link>
            </div>

            <div className="hidden lg:flex items-center gap-4">
              <DesktopNav />
              <div className="flex items-center gap-2 shrink-0">{ctaButtons}</div>
            </div>

            <MobileNav ctaButtons={ctaButtons} />
          </div>
        </div>
      </div>
    </header>
  );
}
