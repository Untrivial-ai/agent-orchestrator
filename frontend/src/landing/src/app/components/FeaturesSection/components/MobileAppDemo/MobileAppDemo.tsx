"use client";

import { AnimatePresence, motion } from "motion/react";
import {
  Activity,
  Bell,
  Check,
  ChevronDown,
  ChevronRight,
  Clock,
  ExternalLink,
  Folder,
  GitBranch,
  GitPullRequest,
  Info,
  Layers,
  Link2,
  Moon,
  Plus,
  RotateCcw,
  Settings,
  Share2,
  Terminal,
  Wifi,
} from "lucide-react";
import { useMemo, useState } from "react";

// Palette copied from packages/mobile/lib/theme.ts (darkTheme) so the mockup and
// the shipped app cannot drift apart.
const t = {
  bgBase: "#0a0b0d",
  bgSurface: "#121317",
  bgElevated: "#15171b",
  bgElevatedHover: "#191b20",
  textPrimary: "#f4f5f7",
  textSecondary: "#9ba1aa",
  textTertiary: "#646a73",
  textFaint: "#444951",
  borderSubtle: "rgba(255,255,255,0.06)",
  borderDefault: "rgba(255,255,255,0.10)",
  bgSubtle: "rgba(255,255,255,0.04)",
  blue: "#4d8dff",
  tintBlue: "rgba(77,141,255,0.14)",
  orange: "#f59f4c",
  tintOrange: "rgba(245,159,76,0.14)",
  amber: "#e8c14a",
  green: "#74b98a",
  tintGreen: "rgba(116,185,138,0.14)",
  purple: "#a78bfa",
  red: "#ef6b73",
} as const;

/**
 * The screen is authored once at this logical size and the whole phone is scaled
 * to fit its slot, so every metric inside is a single constant instead of a set
 * of per-breakpoint values. 280x606 keeps the 0.462 aspect of a modern iPhone;
 * the type is drawn ~15% larger than the real app's points because a 190px-wide
 * render of a 390pt screen is unreadable — a marketing zoom, not a redesign.
 */
const SCREEN_W = 280;
const SCREEN_H = 606;
const BEZEL = 9;
const FRAME_W = SCREEN_W + BEZEL * 2;
const FRAME_H = SCREEN_H + BEZEL * 2;

type Zone = "action" | "working" | "merge" | "done";
type Tab = "Agents" | "Orchestrator" | "PRs" | "Settings";
type ProjectId = "all" | "agent-orchestrator-mo" | "meetyou" | "adorable";

type Session = {
  id: number;
  project: Exclude<ProjectId, "all">;
  /** Middle-elided, the way the app's `shortLabel` renders a long project id. */
  projectLabel: string;
  title: string;
  branch: string;
  zone: Zone;
  /** The card's own state, which is finer-grained than its board zone. */
  status: string;
  statusColor: string;
  breathing?: boolean;
  time: string;
  /** Harness mark. `dark` marks are white glyphs and need a chip behind them. */
  icon: string;
  chip?: "dark";
  pr?: { text: string; color: string };
};

const sessions: Session[] = [
  {
    id: 16,
    project: "agent-orchestrator-mo",
    projectLabel: "agent-orch…trator-mo",
    title: "auth migration",
    branch: "ao/agent-orchestrator-mo-16/root",
    zone: "action",
    status: "Needs input",
    statusColor: t.amber,
    time: "4m",
    icon: "/app-icons/coverage-claude-code.svg",
  },
  {
    id: 17,
    project: "agent-orchestrator-mo",
    projectLabel: "agent-orch…trator-mo",
    title: "mobile revamp",
    branch: "ao/agent-orchestrator-mo-17/root",
    zone: "working",
    status: "Working",
    statusColor: t.orange,
    breathing: true,
    time: "1m",
    icon: "/app-icons/coverage-claude-code.svg",
  },
  {
    id: 18,
    project: "agent-orchestrator-mo",
    projectLabel: "agent-orch…trator-mo",
    title: "onboarding",
    branch: "ao/agent-orchestrator-mo-18/root",
    zone: "working",
    status: "No signal",
    statusColor: t.textTertiary,
    time: "6h",
    icon: "/app-icons/coverage-codex.svg",
  },
  {
    id: 9,
    project: "meetyou",
    projectLabel: "meetyou",
    title: "push notifications",
    branch: "ao/meetyou-9/root",
    zone: "working",
    status: "Working",
    statusColor: t.orange,
    breathing: true,
    time: "12m",
    icon: "/app-icons/coverage-cursor.svg",
    chip: "dark",
  },
  {
    id: 6,
    project: "meetyou",
    projectLabel: "meetyou",
    title: "fix-pin-unpin",
    branch: "ao/meetyou-6/pin-fix-and-restore",
    zone: "merge",
    status: "Merged",
    statusColor: t.green,
    time: "6h",
    icon: "/app-icons/coverage-claude-code.svg",
    pr: { text: "PR #4, #3 merged", color: t.textSecondary },
  },
  {
    id: 2,
    project: "adorable",
    projectLabel: "adorable",
    title: "landing copy pass",
    branch: "ao/adorable-2/root",
    zone: "working",
    status: "Working",
    statusColor: t.orange,
    breathing: true,
    time: "22m",
    icon: "/app-icons/coverage-opencode.svg",
    chip: "dark",
  },
];

const zoneMeta: Record<Zone, { label: string; color: string }> = {
  action: { label: "Needs you", color: t.amber },
  working: { label: "Working", color: t.orange },
  merge: { label: "Ready to merge", color: t.green },
  done: { label: "Done", color: t.textTertiary },
};

const projects: { id: ProjectId; name: string; hint?: string }[] = [
  { id: "all", name: "All projects" },
  {
    id: "agent-orchestrator-mo",
    name: "agent-orchestrator-mo",
    hint: "agent-orches",
  },
  { id: "meetyou", name: "meetyou", hint: "meetyou" },
  { id: "adorable", name: "adorable", hint: "adorable" },
];

const tabs: { label: Tab; icon: typeof Activity }[] = [
  { label: "Agents", icon: Activity },
  { label: "Orchestrator", icon: Share2 },
  { label: "PRs", icon: GitPullRequest },
  { label: "Settings", icon: Settings },
];

export function MobileAppDemo() {
  // The direction rides along with the tab so the outgoing and incoming screens
  // slide the same way the tab bar reads: forward for a tab to the right.
  const [{ direction, tab }, setTab] = useState<{
    direction: 1 | -1;
    tab: Tab;
  }>({ direction: 1, tab: "Agents" });
  const [project, setProject] = useState<ProjectId>("all");
  const [selected, setSelected] = useState<number | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);

  const goToTab = (next: Tab) => {
    setTab((prev) => ({
      tab: next,
      direction:
        tabs.findIndex((x) => x.label === next) >
        tabs.findIndex((x) => x.label === prev.tab)
          ? 1
          : -1,
    }));
  };

  return (
    <div className="relative flex h-[300px] w-full items-center justify-center sm:h-[380px] lg:h-[420px]">
      {/* Ambient bloom, so the phone sits in the scene instead of on top of it. */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute size-[260px] rounded-full bg-[#4d8dff] opacity-[0.13] blur-[70px]"
      />

      <div
        className="relative shrink-0 origin-center scale-[0.481] sm:scale-[0.609] lg:scale-[0.673]"
        style={{ width: FRAME_W, height: FRAME_H }}
      >
        <PhoneFrame>
          <StatusBar />

          <div className="relative min-h-0 flex-1 overflow-hidden">
            {/* Both screens are absolute, so they cross-slide together the way a
                native pager does rather than waiting for each other. */}
            <AnimatePresence initial={false} custom={direction}>
              <motion.div
                key={tab}
                custom={direction}
                variants={{
                  enter: (d: 1 | -1) => ({ x: `${d * 100}%`, opacity: 0 }),
                  center: { x: "0%", opacity: 1 },
                  exit: (d: 1 | -1) => ({ x: `${d * -100}%`, opacity: 0 }),
                }}
                initial="enter"
                animate="center"
                exit="exit"
                transition={{ type: "spring", duration: 0.42, bounce: 0.08 }}
                className="absolute inset-0 flex flex-col"
              >
                {tab === "Agents" ? (
                  <AgentsScreen
                    project={project}
                    selected={selected}
                    onSelect={setSelected}
                    onOpenProjects={() => setSheetOpen(true)}
                  />
                ) : tab === "Orchestrator" ? (
                  <OrchestratorScreen />
                ) : tab === "PRs" ? (
                  <PRScreen
                    project={project}
                    onOpenProjects={() => setSheetOpen(true)}
                  />
                ) : (
                  <SettingsScreen
                    project={project}
                    onOpenProjects={() => setSheetOpen(true)}
                  />
                )}
              </motion.div>
            </AnimatePresence>
          </div>

          <TabBar active={tab} onChange={goToTab} />

          {/* Above the tab bar, like the app's native form sheet. */}
          <ProjectSheet
            open={sheetOpen}
            project={project}
            onSelect={setProject}
            onClose={() => setSheetOpen(false)}
          />
        </PhoneFrame>
      </div>
    </div>
  );
}

/* ------------------------------------------------------------------ device */

function PhoneFrame({ children }: { children: React.ReactNode }) {
  return (
    <div className="relative h-full w-full">
      {/* Side buttons: three on the left rail, one on the right, drawn as thin
          gradient slivers so the silhouette reads as hardware at any scale. */}
      <span
        aria-hidden="true"
        className="absolute -left-[2px] top-[96px] h-[22px] w-[3px] rounded-l-[2px] bg-gradient-to-r from-[#575c66] to-[#22252a]"
      />
      <span
        aria-hidden="true"
        className="absolute -left-[2px] top-[142px] h-[38px] w-[3px] rounded-l-[2px] bg-gradient-to-r from-[#575c66] to-[#22252a]"
      />
      <span
        aria-hidden="true"
        className="absolute -left-[2px] top-[192px] h-[38px] w-[3px] rounded-l-[2px] bg-gradient-to-r from-[#575c66] to-[#22252a]"
      />
      <span
        aria-hidden="true"
        className="absolute -right-[2px] top-[168px] h-[58px] w-[3px] rounded-r-[2px] bg-gradient-to-l from-[#575c66] to-[#22252a]"
      />

      <div
        className="relative h-full w-full rounded-[46px] p-[9px] shadow-[0_1px_1px_rgba(255,255,255,0.16)_inset,0_-1px_1px_rgba(255,255,255,0.06)_inset,0_36px_70px_-22px_rgba(0,0,0,0.85),0_12px_28px_-8px_rgba(0,0,0,0.55)]"
        style={{
          background:
            "linear-gradient(152deg,#63686f 0%,#2a2d33 22%,#16181c 52%,#23262b 78%,#4c5158 100%)",
        }}
      >
        <div
          className="relative flex h-full w-full flex-col overflow-hidden rounded-[38px] font-sans antialiased"
          style={{ backgroundColor: t.bgBase, color: t.textPrimary }}
        >
          {children}

          {/* Dynamic island, home indicator and a single soft glare — the three
              cues that make a rectangle read as a phone. All non-interactive and
              drawn above the UI. */}
          <span
            aria-hidden="true"
            className="pointer-events-none absolute left-1/2 top-[9px] z-30 h-[24px] w-[84px] -translate-x-1/2 rounded-full bg-black shadow-[0_0_0_1px_rgba(255,255,255,0.04)]"
          />
          <span
            aria-hidden="true"
            className="pointer-events-none absolute bottom-[7px] left-1/2 z-30 h-[4px] w-[100px] -translate-x-1/2 rounded-full bg-white/35"
          />
          <span
            aria-hidden="true"
            className="pointer-events-none absolute -left-[40%] -top-[10%] z-30 h-[130%] w-[70%] rotate-[18deg] bg-gradient-to-r from-transparent via-white/[0.045] to-transparent"
          />
          <span
            aria-hidden="true"
            className="pointer-events-none absolute inset-0 z-30 rounded-[38px] ring-1 ring-inset ring-white/[0.07]"
          />
        </div>
      </div>
    </div>
  );
}

function StatusBar() {
  return (
    <div className="relative z-10 flex h-[34px] shrink-0 items-center justify-between px-[22px] pt-[4px]">
      <span className="text-[11px] font-semibold tabular-nums tracking-[-0.2px]">
        9:41
      </span>
      <div className="flex items-center gap-[5px]" aria-hidden="true">
        <span className="flex items-end gap-[1.5px]">
          {[3, 5, 7, 9].map((h) => (
            <i
              key={h}
              className="w-[2px] rounded-[1px] bg-white"
              style={{ height: h }}
            />
          ))}
        </span>
        <Wifi className="size-[11px]" strokeWidth={2.4} />
        <span className="flex h-[10px] w-[19px] items-center rounded-[3px] border border-white/45 p-[1.5px]">
          <i className="block h-full w-[75%] rounded-[1px] bg-white" />
        </span>
      </div>
    </div>
  );
}

/* ------------------------------------------------------------------ shared */

function ScreenHeader({
  title,
  subtitle,
  right,
}: {
  title: string;
  subtitle?: string;
  right?: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-[10px] px-[14px] pb-[8px] pt-[6px]">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-[7px]">
          <h4 className="text-[22px] font-extrabold leading-none tracking-[-0.6px]">
            {title}
          </h4>
          {/* The mascot's wand tip doubles as the connection lamp, exactly as in
              the app's ScreenHeader. */}
          <span className="relative block size-[22px]">
            <img src="/ao-logo.svg" alt="" className="size-full" draggable="false" loading="lazy" decoding="async" />
            <span
              className="absolute -right-[1px] -top-[2px] size-[5px] rounded-full"
              style={{
                backgroundColor: t.green,
                boxShadow: `0 0 7px 2px rgba(116,185,138,0.45)`,
              }}
            />
          </span>
        </div>
        {subtitle ? (
          <p
            className="mt-[3px] truncate font-mono text-[9.5px]"
            style={{ color: t.textTertiary }}
          >
            {subtitle}
          </p>
        ) : null}
      </div>
      {right}
    </div>
  );
}

function SectionHeader({
  label,
  color,
  count,
}: {
  label: string;
  color: string;
  count: number;
}) {
  return (
    <div className="flex items-center gap-[7px] px-[14px] pb-[6px] pt-[12px]">
      <span
        className="h-[10px] w-[2.5px] rounded-full"
        style={{ backgroundColor: color }}
      />
      <span
        className="flex-1 text-[9px] font-bold uppercase tracking-[1.1px]"
        style={{ color: t.textSecondary }}
      >
        {label}
      </span>
      <span
        className="font-mono text-[9px] font-bold tabular-nums"
        style={{ color: t.textTertiary }}
      >
        {count}
      </span>
    </div>
  );
}

function HarnessMark({
  chip,
  size = 17,
  src,
}: {
  chip?: "dark";
  size?: number;
  src: string;
}) {
  // A chip only where the mark needs one to stay visible (white glyphs); most
  // marks are colourful enough to render bare, and a chip behind every one of
  // them would turn a calm column of logos into a column of boxes.
  const inset = chip === "dark" ? Math.round(size * 0.28) : 0;
  return (
    <span
      className="grid shrink-0 place-items-center rounded-[5px]"
      style={{
        width: size,
        height: size,
        backgroundColor: chip === "dark" ? "#24272e" : "transparent",
      }}
    >
      <img
        src={src}
        alt=""
        draggable="false"
        loading="lazy"
        decoding="async"
        style={{ width: size - inset, height: size - inset }}
      />
    </span>
  );
}

function Dot({
  breathing,
  color,
  size = 6,
}: {
  breathing?: boolean;
  color: string;
  size?: number;
}) {
  return (
    <span
      className="relative flex shrink-0"
      style={{ width: size, height: size }}
    >
      {breathing ? (
        <span
          className="absolute inline-flex size-full animate-ping rounded-full opacity-40"
          style={{ backgroundColor: color }}
        />
      ) : null}
      <span
        className="relative rounded-full"
        style={{ width: size, height: size, backgroundColor: color }}
      />
    </span>
  );
}

// Label + current scope + chevron, opening the same sheet Settings uses — one
// project picker in the app rather than two controls that drift apart.
function ProjectSwitcher({
  onOpen,
  project,
}: {
  onOpen: () => void;
  project: ProjectId;
}) {
  const active = projects.find((p) => p.id === project);
  const scoped = project !== "all";

  return (
    <div className="flex items-center gap-[10px] px-[14px] pb-[4px]">
      <span
        className="flex-1 text-[9.5px] font-bold uppercase tracking-[0.9px]"
        style={{ color: t.textSecondary }}
      >
        Projects
      </span>
      <button
        type="button"
        onClick={onOpen}
        className="flex items-center gap-[5px] rounded-[7px] px-[7px] py-[3px] outline-none transition-opacity duration-150 active:opacity-60 focus-visible:ring-1"
        style={{ color: scoped ? t.blue : t.textSecondary }}
      >
        <span className="max-w-[120px] truncate text-[10.5px] font-semibold">
          {active?.name}
        </span>
        <ChevronDown className="size-[12px]" />
      </button>
    </div>
  );
}

// The app presents its pickers as native form sheets: a scrim, a grabber, a
// title block, then the rows. Anchored to the phone so it covers the tab bar,
// which is what a real sheet does.
function ProjectSheet({
  onClose,
  onSelect,
  open,
  project,
}: {
  onClose: () => void;
  onSelect: (id: ProjectId) => void;
  open: boolean;
  project: ProjectId;
}) {
  return (
    <AnimatePresence>
      {open ? (
        <div className="absolute inset-0 z-40">
          <motion.button
            type="button"
            aria-label="Close"
            onClick={onClose}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.2 }}
            className="absolute inset-0 cursor-default bg-black/55 outline-none"
          />

          <motion.div
            initial={{ y: "100%" }}
            animate={{ y: "0%" }}
            exit={{ y: "100%" }}
            transition={{ type: "spring", duration: 0.42, bounce: 0.12 }}
            className="absolute inset-x-0 bottom-0 rounded-t-[16px] px-[20px] pb-[18px] pt-[8px] shadow-[0_-16px_40px_rgba(0,0,0,0.55)]"
            style={{ backgroundColor: t.bgSurface }}
          >
            <span
              aria-hidden="true"
              className="mx-auto mb-[12px] block h-[4px] w-[36px] rounded-full"
              style={{ backgroundColor: t.textFaint }}
            />

            <p className="text-[15px] font-extrabold tracking-[-0.3px]">
              Active project
            </p>
            <p
              className="mt-[2px] text-[10px]"
              style={{ color: t.textTertiary }}
            >
              Scopes the Agents and PRs tabs.
            </p>

            <div className="mt-[10px]">
              {projects.map((p) => {
                const on = p.id === project;
                return (
                  <button
                    key={p.id}
                    type="button"
                    onClick={() => {
                      // Dismiss before reporting the choice, as the app does.
                      onClose();
                      onSelect(p.id);
                    }}
                    className="flex w-full items-center gap-[9px] py-[9px] text-left outline-none transition-opacity duration-150 active:opacity-60 focus-visible:ring-1"
                  >
                    {p.id === "all" ? (
                      <Layers
                        className="size-[13px] shrink-0"
                        style={{ color: on ? t.blue : t.textTertiary }}
                      />
                    ) : (
                      <Folder
                        className="size-[13px] shrink-0"
                        style={{ color: on ? t.blue : t.textTertiary }}
                      />
                    )}
                    <span
                      className="min-w-0 flex-1 truncate text-[12px] font-semibold"
                      style={{ color: on ? t.blue : t.textPrimary }}
                    >
                      {p.name}
                    </span>
                    {p.hint ? (
                      <span
                        className="shrink-0 font-mono text-[9.5px]"
                        style={{ color: t.textFaint }}
                      >
                        {p.hint}
                      </span>
                    ) : null}
                    {on ? (
                      <Check
                        className="size-[13px] shrink-0"
                        style={{ color: t.blue }}
                        strokeWidth={2.6}
                      />
                    ) : null}
                  </button>
                );
              })}
            </div>
          </motion.div>
        </div>
      ) : null}
    </AnimatePresence>
  );
}

/* ------------------------------------------------------------------ agents */

function AgentsScreen({
  onOpenProjects,
  onSelect,
  project,
  selected,
}: {
  onOpenProjects: () => void;
  onSelect: (id: number | null) => void;
  project: ProjectId;
  selected: number | null;
}) {
  const visible = useMemo(
    () =>
      project === "all"
        ? sessions
        : sessions.filter((s) => s.project === project),
    [project],
  );

  const counts = {
    working: visible.filter((s) => s.zone === "working").length,
    action: visible.filter((s) => s.zone === "action").length,
    merge: visible.filter((s) => s.zone === "merge").length,
  };

  return (
    <div className="relative flex h-full min-h-0 flex-col">
      <ScreenHeader
        title="Agents"
        subtitle="192.168.88.3"
        right={
          <span className="relative block p-[2px]">
            <Bell className="size-[16px]" style={{ color: t.textSecondary }} strokeWidth={2} />
            <span
              className="absolute right-0 top-0 size-[6px] rounded-full"
              style={{ backgroundColor: t.blue }}
            />
          </span>
        }
      />

      <div className="flex gap-[7px] px-[14px] pb-[8px] pt-[2px]">
        <Stat n={counts.working} label="working" color={t.orange} />
        <Stat n={counts.action} label="need you" color={t.amber} />
        <Stat n={counts.merge} label="mergeable" color={t.green} />
      </div>

      <ProjectSwitcher project={project} onOpen={onOpenProjects} />

      <div className="relative min-h-0 flex-1 overflow-hidden">
        {(["action", "working", "merge", "done"] as const).map((zone) => {
          const rows = visible.filter((s) => s.zone === zone);
          if (rows.length === 0) return null;
          return (
            <section key={zone}>
              <SectionHeader
                label={zoneMeta[zone].label}
                color={zoneMeta[zone].color}
                count={rows.length}
              />
              {rows.map((s) => (
                <SessionCard
                  key={`${s.project}:${s.id}`}
                  session={s}
                  selected={selected === s.id}
                  showProject={project === "all"}
                  onClick={() => onSelect(selected === s.id ? null : s.id)}
                />
              ))}
            </section>
          );
        })}

        {/* The board keeps scrolling under the tab bar in the app; the fade is
            what says so instead of a hard crop. */}
        <span
          aria-hidden="true"
          className="pointer-events-none absolute inset-x-0 bottom-0 h-[46px]"
          style={{
            background: `linear-gradient(to top, ${t.bgBase}, rgba(10,11,13,0))`,
          }}
        />
      </div>

      <button
        type="button"
        aria-label="New agent"
        className="absolute bottom-[14px] right-[14px] z-10 grid size-[38px] place-items-center rounded-full outline-none transition-transform duration-150 active:scale-[0.94] focus-visible:ring-2"
        style={{
          backgroundColor: t.blue,
          color: "#07101f",
          boxShadow: "0 8px 20px -4px rgba(77,141,255,0.55)",
        }}
      >
        <Plus className="size-[19px]" strokeWidth={2.4} />
      </button>
    </div>
  );
}

function Stat({
  color,
  label,
  n,
}: {
  color: string;
  label: string;
  n: number;
}) {
  return (
    <div
      className="flex-1 rounded-[10px] border px-[9px] py-[8px]"
      style={{ backgroundColor: t.bgElevated, borderColor: t.borderSubtle }}
    >
      <p
        className="font-mono text-[19px] font-extrabold leading-none tabular-nums"
        style={{ color: n > 0 ? color : t.textFaint }}
      >
        {n}
      </p>
      <p
        className="mt-[4px] text-[9px] font-semibold"
        style={{ color: t.textTertiary }}
      >
        {label}
      </p>
    </div>
  );
}

function SessionCard({
  onClick,
  selected,
  session,
  showProject,
}: {
  onClick: () => void;
  selected: boolean;
  session: Session;
  showProject: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="mx-[12px] my-[4px] block w-[calc(100%-24px)] rounded-[10px] border px-[11px] py-[10px] text-left outline-none transition-[background-color,border-color,transform] duration-150 active:scale-[0.985] focus-visible:ring-1"
      style={{
        backgroundColor: selected ? t.bgElevatedHover : t.bgElevated,
        borderColor: selected ? t.blue : t.borderSubtle,
      }}
    >
      {/* Identity above the hairline, state below — the app's card anatomy. */}
      <span className="flex items-start gap-[8px]">
        <HarnessMark src={session.icon} chip={session.chip} />
        <span className="min-w-0 flex-1 truncate text-[12.5px] font-bold leading-[16px]">
          {session.title}
        </span>
        {showProject ? (
          <span
            /* Never shrinks — the title truncates instead, as in the app. */
            className="mt-[2px] shrink-0 font-mono text-[9px]"
            style={{ color: t.textTertiary }}
          >
            {session.projectLabel}
          </span>
        ) : null}
      </span>

      <span
        className="mt-[5px] flex items-center gap-[5px] pl-[25px] font-mono text-[9px]"
        style={{ color: t.textFaint }}
      >
        <GitBranch className="size-[9px] shrink-0" />
        <span className="min-w-0 flex-1 truncate">{session.branch}</span>
      </span>

      <span
        className="mt-[9px] block h-px w-full"
        style={{ backgroundColor: t.borderSubtle }}
      />

      <span className="mt-[7px] flex items-center gap-[5px]">
        <Dot color={session.statusColor} breathing={session.breathing} />
        <span
          className="text-[10px] font-semibold"
          style={{ color: session.statusColor }}
        >
          {session.status}
        </span>
        <span className="flex-1" />
        <span className="font-mono text-[9px]" style={{ color: t.textFaint }}>
          {session.time}
        </span>
      </span>

      {session.pr ? (
        <span
          className="mt-[4px] block font-mono text-[9px]"
          style={{ color: session.pr.color }}
        >
          {session.pr.text}
        </span>
      ) : null}
    </button>
  );
}

/* ------------------------------------------------------------ orchestrator */

// One card per project, mirroring the app's OrchestratorCard: the mark and the
// project name, the orchestrator's own state, a row of zone pills counting its
// workers, then a footer of worker count + actions.
const orchestrators: {
  project: string;
  harness: string;
  icon: string;
  chip?: "dark";
  zones: { n: number; label: string; color: string; tint: string }[];
  workers: number;
}[] = [
  {
    project: "agent-orchestrator-mo",
    harness: "claude-code",
    icon: "/app-icons/coverage-claude-code.svg",
    zones: [
      { n: 3, label: "Working", color: t.orange, tint: t.tintOrange },
      { n: 8, label: "Done", color: t.textTertiary, tint: t.bgSubtle },
    ],
    workers: 11,
  },
  {
    project: "meetyou",
    harness: "claude-code",
    icon: "/app-icons/coverage-claude-code.svg",
    zones: [
      { n: 1, label: "Working", color: t.orange, tint: t.tintOrange },
      { n: 5, label: "Done", color: t.textTertiary, tint: t.bgSubtle },
    ],
    workers: 6,
  },
  {
    project: "oddswell",
    harness: "codex",
    icon: "/app-icons/coverage-codex.svg",
    zones: [{ n: 9, label: "Done", color: t.textTertiary, tint: t.bgSubtle }],
    workers: 9,
  },
  {
    project: "precision-market",
    harness: "claude-code",
    icon: "/app-icons/coverage-claude-code.svg",
    zones: [
      { n: 1, label: "Ready to merge", color: t.green, tint: t.tintGreen },
      { n: 5, label: "Done", color: t.textTertiary, tint: t.bgSubtle },
    ],
    workers: 6,
  },
];

function OrchestratorScreen() {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <ScreenHeader title="Orchestrator" />

      {/* Deliberately every project, ignoring the active-project filter the
          other tabs honour — this tab is the cross-project overview. */}
      <div className="relative min-h-0 flex-1 overflow-hidden pt-[4px]">
        {orchestrators.map((o) => (
          <div
            key={o.project}
            className="mx-[12px] my-[5px] rounded-[10px] border px-[11px] py-[10px]"
            style={{ backgroundColor: t.bgElevated, borderColor: t.borderSubtle }}
          >
            <div className="flex items-center gap-[8px]">
              <HarnessMark src={o.icon} chip={o.chip} size={21} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-[12.5px] font-bold leading-[15px]">
                  {o.project}
                </p>
                <div className="mt-[3px] flex items-center gap-[5px]">
                  <Dot color={t.textTertiary} />
                  <span
                    className="text-[10px] font-semibold"
                    style={{ color: t.textTertiary }}
                  >
                    Idle
                  </span>
                  <span
                    className="truncate font-mono text-[10px]"
                    style={{ color: t.textTertiary }}
                  >
                    · {o.harness}
                  </span>
                </div>
              </div>
            </div>

            <div className="mt-[9px] flex flex-wrap gap-[5px]">
              {o.zones.map((z) => (
                <span
                  key={z.label}
                  className="flex items-center gap-[4px] rounded-[7px] px-[7px] py-[4px]"
                  style={{ backgroundColor: z.tint }}
                >
                  <Dot color={z.color} size={5} />
                  <span
                    className="font-mono text-[10px] font-extrabold"
                    style={{ color: z.color }}
                  >
                    {z.n}
                  </span>
                  <span
                    className="text-[9.5px] font-semibold"
                    style={{ color: t.textSecondary }}
                  >
                    {z.label}
                  </span>
                </span>
              ))}
            </div>

            <div className="mt-[9px] flex items-center gap-[8px]">
              <span
                className="flex-1 text-[10px]"
                style={{ color: t.textTertiary }}
              >
                {o.workers} workers
              </span>
              <IconButton icon={RotateCcw} />
              <IconButton icon={Terminal} />
            </div>
          </div>
        ))}

        <BottomFade />
      </div>
    </div>
  );
}

// The app's bordered square action — deliberately not a filled button: these
// are secondary actions sitting inside a card.
function IconButton({ icon: Icon }: { icon: typeof Terminal }) {
  return (
    <span
      className="grid size-[25px] shrink-0 place-items-center rounded-[7px] border"
      style={{ borderColor: t.borderDefault, color: t.textSecondary }}
    >
      <Icon className="size-[12px]" strokeWidth={2} />
    </span>
  );
}

function BottomFade() {
  return (
    <span
      aria-hidden="true"
      className="pointer-events-none absolute inset-x-0 bottom-0 h-[46px]"
      style={{
        background: `linear-gradient(to top, ${t.bgBase}, rgba(10,11,13,0))`,
      }}
    />
  );
}

/* --------------------------------------------------------------------- prs */

type PRFilter = "Open" | "Merged" | "All";

const pullRequests: {
  number: number;
  state: string;
  color: string;
  repo: string;
  title: string;
  meta: string;
  files?: number;
  added?: number;
  removed?: number;
  status: string;
  statusColor: string;
  buckets: PRFilter[];
}[] = [
  {
    number: 3,
    state: "open",
    color: t.green,
    repo: "Prasad-D-W…/pinpoint",
    title: "feat(mobile): push-to-talk dictation",
    meta: "ao/precision-market-4/dictation → main · Prasad-D-Ware",
    status: "CI passing · Mergeable",
    statusColor: t.green,
    buckets: ["Open", "All"],
  },
  {
    number: 32,
    state: "merged",
    color: t.purple,
    repo: "Zerith-Stu…on-market",
    title: "feat(web): settlement-tx link on closed markets",
    meta: "ao/prediction-market-12/settle-tx → main · Prasad-D-…",
    files: 7,
    added: 322,
    removed: 39,
    status: "Merged",
    statusColor: t.green,
    buckets: ["Merged", "All"],
  },
  {
    number: 30,
    state: "merged",
    color: t.purple,
    repo: "Zerith-Stu…on-market",
    title: "docs: clean up docs-site into a standalone reference",
    meta: "ao/prediction-market-11/docs-cleanup → main · Prasad…",
    files: 11,
    added: 72,
    removed: 141,
    status: "Merged",
    statusColor: t.green,
    buckets: ["Merged", "All"],
  },
];

function PRScreen({
  onOpenProjects,
  project,
}: {
  onOpenProjects: () => void;
  project: ProjectId;
}) {
  const [filter, setFilter] = useState<PRFilter>("All");
  const counts = { Open: 1, Merged: 10, All: 12 };
  const visible = pullRequests.filter((pr) => pr.buckets.includes(filter));

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ScreenHeader title="Pull Requests" />

      <ProjectSwitcher project={project} onOpen={onOpenProjects} />

      <div className="flex gap-[6px] px-[14px] pb-[8px]">
        {(["Open", "Merged", "All"] as PRFilter[]).map((f) => {
          const on = f === filter;
          return (
            <button
              key={f}
              type="button"
              onClick={() => setFilter(f)}
              className="rounded-full border px-[11px] py-[5px] text-[10px] font-semibold outline-none transition-colors duration-150 focus-visible:ring-1"
              style={{
                backgroundColor: on ? t.tintBlue : t.bgElevated,
                borderColor: on ? t.blue : t.borderDefault,
                color: on ? t.blue : t.textSecondary,
              }}
            >
              {f} {counts[f]}
            </button>
          );
        })}
      </div>

      <div className="relative min-h-0 flex-1 overflow-hidden">
        {visible.map((pr) => (
          <div
            key={pr.number}
            className="mx-[12px] my-[5px] rounded-[10px] border px-[11px] py-[10px]"
            style={{ backgroundColor: t.bgElevated, borderColor: t.borderSubtle }}
          >
            <div className="flex items-center gap-[6px]">
              <GitPullRequest className="size-[11px]" style={{ color: pr.color }} />
              <span
                className="font-mono text-[10px] font-bold"
                style={{ color: t.textSecondary }}
              >
                #{pr.number}
              </span>
              <span className="text-[10px] font-semibold" style={{ color: pr.color }}>
                {pr.state}
              </span>
              <span className="flex-1" />
              <span
                className="shrink-0 font-mono text-[9px]"
                style={{ color: t.textTertiary }}
              >
                {pr.repo}
              </span>
            </div>

            <p className="mt-[6px] text-[12px] font-bold leading-[16px]">
              {pr.title}
            </p>

            <p
              className="mt-[4px] truncate font-mono text-[9px]"
              style={{ color: t.textFaint }}
            >
              {pr.meta}
            </p>

            {pr.files ? (
              <div className="mt-[5px] flex items-center gap-[8px] font-mono text-[9px]">
                <span style={{ color: t.textTertiary }}>{pr.files} files</span>
                <span style={{ color: t.green }}>+{pr.added}</span>
                <span style={{ color: t.red }}>−{pr.removed}</span>
              </div>
            ) : null}

            <div className="mt-[9px] flex items-center gap-[8px]">
              <span
                className="flex-1 text-[10px] font-semibold"
                style={{ color: pr.statusColor }}
              >
                {pr.status}
              </span>
              <IconButton icon={Terminal} />
              <IconButton icon={ExternalLink} />
            </div>
          </div>
        ))}

        <BottomFade />
      </div>
    </div>
  );
}

/* ---------------------------------------------------------------- settings */

function SettingsScreen({
  onOpenProjects,
  project,
}: {
  onOpenProjects: () => void;
  project: ProjectId;
}) {
  const active = projects.find((p) => p.id === project);

  return (
    <div className="relative flex h-full min-h-0 flex-col overflow-hidden">
      <ScreenHeader title="Settings" />

      <div className="min-h-0 flex-1 overflow-hidden px-[14px] pt-[4px]">
        <SettingsGroup
          label="Connection"
          footer="Your PC's Tailscale name / 100.x address, or its LAN IP on the same Wi-Fi."
        >
          <SettingsRow
            icon={Link2}
            label="Connect AO"
            value="192.168.88.3:3011"
            lamp
            chevron
          />
          <SettingsRow icon={Activity} label="Test connection" chevron />
        </SettingsGroup>

        <SettingsGroup label="Projects" footer="Scopes the Agents and PRs tabs.">
          <SettingsRow
            icon={Folder}
            label="Active project"
            value={active?.name ?? "All projects"}
            chevron
            onClick={onOpenProjects}
          />
        </SettingsGroup>

        <SettingsGroup label="Appearance">
          <SettingsRow icon={Moon} label="Theme" value="Dark" chevron />
        </SettingsGroup>

        <SettingsGroup label="Notifications">
          <SettingsRow icon={Bell} label="Agent notifications" toggle />
          <SettingsRow icon={Clock} label="History" chevron />
        </SettingsGroup>

        <SettingsGroup label="About">
          <SettingsRow icon={Info} label="Version" value="1.0.0" />
        </SettingsGroup>
      </div>

      <BottomFade />
    </div>
  );
}

function SettingsGroup({
  children,
  footer,
  label,
}: {
  children: React.ReactNode;
  footer?: string;
  label: string;
}) {
  return (
    <div className="mb-[12px]">
      <p
        className="mb-[6px] ml-[3px] text-[9px] font-bold uppercase tracking-[1.1px]"
        style={{ color: t.textTertiary }}
      >
        {label}
      </p>
      <div
        className="overflow-hidden rounded-[10px] border"
        style={{ backgroundColor: t.bgElevated, borderColor: t.borderSubtle }}
      >
        {children}
      </div>
      {footer ? (
        <p
          className="mx-[3px] mt-[6px] text-[9.5px] leading-[13px]"
          style={{ color: t.textTertiary }}
        >
          {footer}
        </p>
      ) : null}
    </div>
  );
}

function SettingsRow({
  chevron,
  icon: Icon,
  label,
  lamp,
  onClick,
  toggle,
  value,
}: {
  chevron?: boolean;
  icon: typeof Info;
  label: string;
  /** Green dot before the value — the connection row's reachability lamp. */
  lamp?: boolean;
  onClick?: () => void;
  toggle?: boolean;
  value?: string;
}) {
  const Tag = onClick ? "button" : "div";
  return (
    <Tag
      type={onClick ? "button" : undefined}
      onClick={onClick}
      // The separator is inset to the label's x-origin (row padding + icon +
      // gap), the iOS detail that makes a stack of rows read as one grouped list.
      className="relative flex items-center gap-[8px] px-[11px] py-[9px] after:absolute after:bottom-0 after:left-[32px] after:right-0 after:h-px after:bg-[rgba(255,255,255,0.06)] last:after:hidden"
    >
      <Icon className="size-[13px] shrink-0" style={{ color: t.textSecondary }} strokeWidth={2} />
      <span className="min-w-0 flex-1 truncate text-[12px] font-semibold">
        {label}
      </span>
      {lamp ? <Dot color={t.green} size={5} /> : null}
      {value ? (
        <span
          className="shrink-0 text-[10.5px]"
          style={{ color: t.textTertiary }}
        >
          {value}
        </span>
      ) : null}
      {toggle ? (
        <span
          className="flex h-[16px] w-[27px] shrink-0 items-center justify-end rounded-full px-[2px]"
          style={{ backgroundColor: t.blue }}
        >
          <i className="block size-[12px] rounded-full bg-white" />
        </span>
      ) : null}
      {chevron ? (
        <ChevronRight className="size-[12px] shrink-0" style={{ color: t.textFaint }} />
      ) : null}
    </Tag>
  );
}

function TabBar({
  active,
  onChange,
}: {
  active: Tab;
  onChange: (tab: Tab) => void;
}) {
  return (
    <div
      className="relative z-10 grid shrink-0 grid-cols-4 border-t pb-[16px] pt-[8px]"
      style={{ backgroundColor: t.bgSurface, borderColor: t.borderSubtle }}
    >
      {tabs.map(({ icon: Icon, label }) => {
        const on = label === active;
        return (
          <button
            key={label}
            type="button"
            onClick={() => onChange(label)}
            className="flex flex-col items-center justify-center gap-[3px] outline-none transition-[color,transform] duration-150 active:scale-[0.94] focus-visible:ring-1"
            style={{ color: on ? t.blue : t.textTertiary }}
          >
            <Icon className="size-[16px]" strokeWidth={on ? 2.3 : 1.9} />
            <span className="text-[8.5px] font-semibold">{label}</span>
          </button>
        );
      })}
    </div>
  );
}
