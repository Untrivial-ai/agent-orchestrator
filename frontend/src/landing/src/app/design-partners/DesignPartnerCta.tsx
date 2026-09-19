"use client";

import type { ReactNode } from "react";

/**
 * A design-partner call to action. There is no form on this page: the two ways
 * to raise your hand are booking a call and writing an email.
 */
export function DesignPartnerCta({
  href,
  className,
  external,
  children,
}: {
  href: string;
  className?: string;
  external?: boolean;
  children: ReactNode;
}) {
  return (
    <a
      href={href}
      className={className}
      {...(external ? { target: "_blank", rel: "noopener noreferrer" } : {})}
    >
      {children}
    </a>
  );
}
