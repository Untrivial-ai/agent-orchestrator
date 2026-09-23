import Link from "next/link";
import { isValidElement, type ReactNode } from "react";
import { FaApple, FaLinux, FaWindows } from "react-icons/fa";
import { slugify } from "@/lib/content-utils";
import { getPlatformDownloads, getReleases } from "@/lib/releases";
import { Tab, Tabs } from "./DocsTabs";

// Text of a heading's children, so ids match the TOC's slugify(headingText).
function textOf(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (isValidElement(node)) return textOf((node.props as { children?: ReactNode }).children);
  return "";
}

function Heading({ level, children }: { level: 2 | 3 | 4; children: ReactNode }) {
  const id = slugify(textOf(children));
  if (level === 2) return <h2 id={id}>{children}</h2>;
  if (level === 3) return <h3 id={id}>{children}</h3>;
  return <h4 id={id}>{children}</h4>;
}

// Brands with a real asset under /public/docs/logos/ (others fall back to a monogram).
const FILE_LOGOS: Record<string, string> = {
  aider: "aider.png",
  "claude-code": "claude-code.svg",
  claude: "claude-code.svg",
  codex: "codex.svg",
  cursor: "cursor.svg",
  opencode: "opencode.svg",
};

export function Logo({ name, size = 20, className }: { name: string; size?: number; className?: string }) {
  const key = name.toLowerCase();
  const file = FILE_LOGOS[key];
  if (file) {
    return (
      // eslint-disable-next-line @next/next/no-img-element
      <img
        src={`/docs/logos/${file}`}
        alt=""
        aria-hidden="true"
        className={className}
        style={{ width: size, height: size, flexShrink: 0, objectFit: "contain" }}
      />
    );
  }
  return (
    <span
      aria-hidden="true"
      className={className}
      style={{ width: size, height: size }}
      // biome-ignore lint/style/useNamingConvention: inline style keys
    >
      <span className="grid size-full place-items-center rounded-sm bg-surface text-[0.6em] font-bold uppercase text-foreground">
        {name.charAt(0)}
      </span>
    </span>
  );
}

const CALLOUT_TONE: Record<string, string> = {
  info: "border-border bg-muted/35",
  warn: "border-border bg-muted/35",
  warning: "border-border bg-muted/35",
  error: "border-border bg-muted/35",
};

export function Callout({ type = "info", title, children }: { type?: string; title?: ReactNode; children: ReactNode }) {
  return (
    <div className={`my-6 rounded-xl border px-4 py-3 ${CALLOUT_TONE[type] ?? CALLOUT_TONE.info}`}>
      {title && <div className="mb-1 text-sm font-semibold text-foreground">{title}</div>}
      <div className="text-sm text-muted-foreground [&>*:first-child]:mt-0 [&>*:last-child]:mb-0">{children}</div>
    </div>
  );
}

export function Accordions({ children }: { children: ReactNode }) {
  return <div className="my-6 space-y-2">{children}</div>;
}

export function Accordion({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <details className="group rounded-xl border border-border bg-muted/30 px-4 py-3">
      <summary className="cursor-pointer list-none text-sm font-medium text-foreground marker:hidden">
        {title}
      </summary>
      <div className="mt-3 text-sm text-muted-foreground [&>*:first-child]:mt-0 [&>*:last-child]:mb-0">{children}</div>
    </details>
  );
}

export function Steps({ children }: { children: ReactNode }) {
  return (
    <div className="my-6 ml-3 border-l border-border/80 pl-6 [counter-reset:step] [&>*]:relative [&>*]:mb-6 [&>*]:before:absolute [&>*]:before:-left-[2.1rem] [&>*]:before:grid [&>*]:before:size-6 [&>*]:before:place-items-center [&>*]:before:rounded-full [&>*]:before:border [&>*]:before:border-border [&>*]:before:bg-background [&>*]:before:text-xs [&>*]:before:text-muted-foreground [&>*]:before:[counter-increment:step] [&>*]:before:[content:counter(step)]">
      {children}
    </div>
  );
}

export function Step({ children }: { children: ReactNode }) {
  return <div className="[&>*:first-child]:mt-0">{children}</div>;
}

export function Cards({ children }: { children: ReactNode }) {
  return <div className="my-6 grid gap-3 sm:grid-cols-2">{children}</div>;
}

export function Card({
  title,
  href,
  description,
  children,
}: {
  title: string;
  href?: string;
  description?: ReactNode;
  children?: ReactNode;
}) {
  const body = (
    <>
      <div className="text-sm font-semibold text-foreground">{title}</div>
      <div className="mt-1 text-sm text-muted-foreground">{description ?? children}</div>
    </>
  );
  const cls =
    "block rounded-xl border border-border bg-muted/30 p-4 no-underline transition-colors hover:bg-muted/45";
  return href ? (
    <Link href={href} className={cls}>
      {body}
    </Link>
  ) : (
    <div className={cls}>{body}</div>
  );
}

export function PluginGrid({ children }: { children: ReactNode }) {
  return (
    <div className="my-6 grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(260px,1fr))]">{children}</div>
  );
}

export function PluginCard({
  name,
  logo,
  href,
  description,
  badge,
}: {
  name: string;
  logo: string;
  href: string;
  description: string;
  badge?: ReactNode;
}) {
  return (
    <Link
      href={href}
      className="flex items-start gap-3.5 rounded-xl border border-border bg-muted/30 p-4 no-underline transition-colors hover:bg-muted/45"
    >
      <span className="grid size-9 shrink-0 place-items-center rounded-lg border border-border bg-background">
        <Logo name={logo} size={22} />
      </span>
      <span className="flex min-w-0 flex-col gap-1">
        <span className="inline-flex items-center gap-2 text-[0.9375rem] font-semibold text-foreground">
          {name}
          {badge}
        </span>
        <span className="text-[0.8125rem] leading-normal text-muted-foreground">{description}</span>
      </span>
    </Link>
  );
}

type Status = "full" | "partial" | "none";
const STATUS_LABEL: Record<Status, string> = { full: "Supported", partial: "Limited", none: "Not supported" };
const PLATFORM_ICONS = {
  macOS: FaApple,
  Windows: FaWindows,
  Linux: FaLinux,
} as const;

function PlatformCell({ platform, status }: { platform: "macos" | "linux" | "windows"; status: Status }) {
  const title = platform === "macos" ? "macOS" : platform === "linux" ? "Linux" : "Windows";
  const Icon = PLATFORM_ICONS[title];
  return (
    <div className="flex min-w-0 flex-1 items-center gap-3 rounded-xl border border-border bg-muted/30 px-3 py-2.5">
      <Icon className="size-[18px] shrink-0" aria-hidden="true" />
      <div className="flex min-w-0 flex-col">
        <span className="text-[0.8125rem] font-semibold text-foreground">{title}</span>
        <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
          {STATUS_LABEL[status]}
        </span>
      </div>
    </div>
  );
}

export function PlatformSupport({
  macos = "full",
  linux = "full",
  windows = "full",
  note,
}: {
  macos?: Status;
  linux?: Status;
  windows?: Status;
  note?: ReactNode;
}) {
  return (
    <div className="my-5">
      <div className="flex flex-wrap gap-2">
        <PlatformCell platform="macos" status={macos} />
        <PlatformCell platform="linux" status={linux} />
        <PlatformCell platform="windows" status={windows} />
      </div>
      {note && <p className="mt-2 text-[0.8125rem] text-muted-foreground">{note}</p>}
    </div>
  );
}

const RELEASES_URL = "https://github.com/Untrivial-ai/agent-orchestrator/releases";
const CHANNELS = ["Stable", "Nightly"] as const;

export async function InstallDownloads() {
  const releases = await getReleases();
  const platformDownloads = getPlatformDownloads(releases);

  return (
    <div className="my-6">
      <div className="mb-3 flex items-center justify-between">
        <div className="text-sm font-semibold text-foreground">Get Agent Orchestrator</div>
        <a
          href={RELEASES_URL}
          className="inline-flex min-h-10 items-center rounded-lg border border-border bg-background px-3 py-2 text-xs font-medium text-foreground !no-underline transition-[background-color,border-color,transform] duration-150 hover:border-foreground/30 hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background active:scale-[0.96] motion-reduce:transition-none"
        >
          View releases →
        </a>
      </div>
      <div className="grid gap-6 sm:grid-cols-3">
        {platformDownloads.map((platform) => (
          <div key={platform.name}>
            <div className="text-sm font-semibold text-foreground">{platform.name}</div>
            {CHANNELS.map((channel) => {
              const builds = platform.builds.filter((item) => item.channel === channel);
              if (builds.length === 0) return null;
              return (
                <div key={channel} className="mt-3">
                  <div className="text-xs font-medium text-muted-foreground">{channel}</div>
                  <div className="mt-2 space-y-2">
                    {builds.map((downloadBuild) => (
                      <div key={downloadBuild.label}>
                        <a
                          href={downloadBuild.href}
                          download
                          className="flex w-full items-center gap-2 rounded-xl border border-border bg-muted/30 px-4 py-3 text-sm font-medium text-foreground !no-underline transition-colors hover:bg-muted/45 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
                        >
                          {(() => {
                            const Icon =
                              PLATFORM_ICONS[
                                platform.name as keyof typeof PLATFORM_ICONS
                              ];
                            return Icon ? (
                              <Icon className="mr-2 size-4 shrink-0" aria-hidden="true" />
                            ) : null;
                          })()}
                          {downloadBuild.label}
                        </a>
                      </div>
                    ))}
                  </div>
                </div>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}

// Passed to <MDXRemote components={...}>. Keys must match the JSX tags in the MDX.
export const docsMdxComponents = {
  h2: ({ children }: { children: ReactNode }) => <Heading level={2}>{children}</Heading>,
  h3: ({ children }: { children: ReactNode }) => <Heading level={3}>{children}</Heading>,
  h4: ({ children }: { children: ReactNode }) => <Heading level={4}>{children}</Heading>,
  Logo,
  Callout,
  Accordion,
  Accordions,
  Step,
  Steps,
  Tab,
  Tabs,
  Card,
  Cards,
  PluginCard,
  PluginGrid,
  PlatformSupport,
  InstallDownloads,
};
