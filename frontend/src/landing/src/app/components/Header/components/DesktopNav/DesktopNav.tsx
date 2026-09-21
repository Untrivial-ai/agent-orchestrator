"use client";

import { cn } from "@ao/ui/utils";
import { HashLink } from "../../../HashLink/HashLink";
import {
  type NavLink,
  PRODUCT_LINKS,
  RESOURCE_LINKS,
} from "../../constants";

const linkClass = cn(
  "h-8 border border-transparent bg-transparent px-3 text-sm font-normal text-muted-foreground hover:border-white hover:bg-accent/40 hover:text-foreground focus:bg-accent/40 focus:text-foreground inline-flex items-center gap-2 whitespace-nowrap rounded-2xl font-medium transition-colors focus-visible:outline-none no-underline",
);

export function DesktopNav() {
  const links = [...PRODUCT_LINKS, ...RESOURCE_LINKS];

  return (
    <nav className="flex items-center gap-1 select-none">
      {links.map((link) => (
        <NavLinkItem key={link.href} link={link} />
      ))}
    </nav>
  );
}

function NavLinkItem({ link }: { link: NavLink }) {
  if (link.external) {
    return (
      <a
        href={link.href}
        target="_blank"
        rel="noopener noreferrer"
        className={linkClass}
      >
        {link.label}
      </a>
    );
  }
  return (
    <HashLink href={link.href} className={linkClass}>
      {link.label}
    </HashLink>
  );
}
