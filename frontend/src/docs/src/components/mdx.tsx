import Link from "next/link";
import defaultMdxComponents from "fumadocs-ui/mdx";
import { Children, cloneElement, isValidElement, type ComponentPropsWithoutRef, type ReactElement, type ReactNode } from "react";
import type { MDXComponents } from "mdx/types";
import { Tab, Tabs } from "./docs-tabs";

function textOf(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (isValidElement(node)) return textOf((node.props as { children?: ReactNode }).children);
  return "";
}

function slugify(text: string): string {
  return text
    .toLowerCase()
    .trim()
    .replace(/[^\w\s-]/g, "")
    .replace(/\s+/g, "-");
}

function Heading({
  level,
  children,
  ...props
}: {
  level: 2 | 3 | 4;
  children?: ReactNode;
} & ComponentPropsWithoutRef<"h2"> &
  ComponentPropsWithoutRef<"h3"> &
  ComponentPropsWithoutRef<"h4">) {
  const id = slugify(textOf(children));
  if (level === 2) return <h2 id={id} {...props}>{children}</h2>;
  if (level === 3) return <h3 id={id} {...props}>{children}</h3>;
  return <h4 id={id} {...props}>{children}</h4>;
}

const FILE_LOGOS: Record<string, string> = {
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
    <span aria-hidden="true" className={className} style={{ width: size, height: size }}>
      <span className="grid size-full place-items-center rounded-sm bg-muted text-[0.6em] font-bold uppercase text-foreground">
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

export function Accordions({ children, expandAll = false }: { children: ReactNode; expandAll?: boolean }) {
  const items = expandAll
    ? Children.map(children, (child) => {
        if (!isValidElement(child)) return child;
        return cloneElement(child as ReactElement<{ open?: boolean }>, { open: true });
      })
    : children;

  return <div className="my-6 overflow-hidden rounded-xl border border-border bg-muted/30">{items}</div>;
}

export function Accordion({ title, children, open = false }: { title: ReactNode; children: ReactNode; open?: boolean }) {
  return (
    <details open={open} className="group border-b border-border/80 px-4 py-3 last:border-b-0">
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
  const cls = "block rounded-xl border border-border bg-muted/30 p-4 no-underline transition-colors hover:bg-muted/45";

  return href ? (
    <Link href={href} className={cls}>
      {body}
    </Link>
  ) : (
    <div className={cls}>{body}</div>
  );
}

export function PluginGrid({ children }: { children: ReactNode }) {
  return <div className="my-6 grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(260px,1fr))]">{children}</div>;
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

const STATUS_LABEL: Record<Status, string> = {
  full: "Supported",
  partial: "Limited",
  none: "Not supported",
};

const STATUS_DOT: Record<Status, string> = {
  full: "bg-green-400",
  partial: "bg-amber-400",
  none: "bg-muted-foreground",
};

function PlatformCell({ platform, status }: { platform: "macos" | "linux" | "windows"; status: Status }) {
  const logoName = platform === "macos" ? "apple" : platform;
  const title = platform === "macos" ? "macOS" : platform === "linux" ? "Linux" : "Windows";

  return (
    <div className="flex min-w-0 flex-1 items-center gap-2 rounded-xl border border-border bg-muted/30 px-3 py-2.5">
      <Logo name={logoName} size={18} />
      <div className="flex min-w-0 flex-col">
        <span className="text-[0.8125rem] font-semibold text-foreground">{title}</span>
        <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
          <span className={`inline-block size-1.5 rounded-full ${STATUS_DOT[status]}`} />
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

export function InstallDownloads() {
  const release = "https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download";
  const downloads = [
    { platform: "macOS Apple silicon", assets: [["Disk image", "darwin-arm64.dmg"], ["Zip", "darwin-arm64.zip"]] },
    { platform: "macOS Intel", assets: [["Disk image", "darwin-x64.dmg"], ["Zip", "darwin-x64.zip"]] },
    { platform: "Windows x64", assets: [["Installer", "win32-x64.exe"]] },
    { platform: "Linux x64", assets: [["AppImage", "linux-x64.AppImage"], ["Debian / Ubuntu", "linux-x64.deb"], ["Fedora / RHEL", "linux-x64.rpm"]] },
  ];

  return (
    <div style={{ marginBlock: "1.25rem", overflowX: "auto" }}>
      <table>
        <caption>Download the latest Agent Orchestrator desktop release</caption>
        <thead>
          <tr><th scope="col">Platform</th><th scope="col">Download</th></tr>
        </thead>
        <tbody>
          {downloads.map(({ platform, assets }) => (
            <tr key={platform}>
              <th scope="row">{platform}</th>
              <td>
                <div style={{ display: "flex", flexWrap: "wrap", columnGap: "0.4rem", rowGap: "0.25rem" }}>
                  {assets.map(([label, asset], index) => (
                    <span key={asset}>
                      {index > 0 && <span aria-hidden="true"> · </span>}
                      <a href={`${release}/agent-orchestrator-${asset}`}>{label}</a>
                    </span>
                  ))}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function getMDXComponents(components?: MDXComponents) {
  return {
    ...defaultMdxComponents,
    h2: (props) => <Heading level={2} {...props} />,
    h3: (props) => <Heading level={3} {...props} />,
    h4: (props) => <Heading level={4} {...props} />,
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
    ...components,
  } satisfies MDXComponents;
}

export const useMDXComponents = getMDXComponents;

declare global {
  type MDXProvidedComponents = ReturnType<typeof getMDXComponents>;
}
