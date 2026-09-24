import { GeistSans } from "geist/font/sans";
import { RootProvider } from "fumadocs-ui/provider/next";
import type { Metadata } from "next";
import type { ReactNode } from "react";
import { COMPANY } from "../lib/constants";
import "./global.css";

export const metadata: Metadata = {
  metadataBase: new URL(COMPANY.DOCS_URL),
  icons: {
    icon: [{ url: `${COMPANY.MARKETING_URL}/favicon.svg`, type: "image/svg+xml" }],
  },
};

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <html
      lang="en"
      className={`dark overscroll-none ${GeistSans.variable}`}
      suppressHydrationWarning
    >
      <body className="min-h-screen bg-background text-foreground antialiased">
        <RootProvider
          theme={{ enabled: false }}
          search={{
            options: {
              type: "static",
              api: "/api/search",
            },
          }}
        >
          {children}
        </RootProvider>
      </body>
    </html>
  );
}
