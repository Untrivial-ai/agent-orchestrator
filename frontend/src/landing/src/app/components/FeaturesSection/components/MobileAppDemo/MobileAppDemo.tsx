"use client";

import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import {
  Bell, Bot, Check, ChevronRight, CircleDot, Folder, GitBranch,
  GitPullRequest, Menu, Plus, Search, Settings, SlidersHorizontal, Wifi, X,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";

// Copied from packages/mobile/lib/theme.ts: this mock follows the shipped app.
const t = {
  bgBase: "#0a0b0d", bgSurface: "#121317", bgElevated: "#15171b",
  bgSubtle: "rgba(255,255,255,0.04)", border: "rgba(255,255,255,0.10)",
  borderSubtle: "rgba(255,255,255,0.06)", text: "#f4f5f7",
  secondary: "#9ba1aa", tertiary: "#646a73", faint: "#444951",
  blue: "#4d8dff", amber: "#e8c14a", orange: "#f59f4c",
  green: "#74b98a", purple: "#a78bfa",
} as const;

const SCREEN_W = 280;
const SCREEN_H = 606;
const FRAME_W = SCREEN_W + 18;
const FRAME_H = SCREEN_H + 18;

/* --------------------------------------------------------------------------
 * ANIMATION STORYBOARD — mobile shell
 *    0ms   drawer backdrop appears; panel springs in from the left
 *    0ms   destination content cross-fades inside the phone
 *  250ms   navigation and content settle
 * Reduced motion resolves every stage immediately.
 * ----------------------------------------------------------------------- */
const MOTION = {
  drawer: { type: "spring" as const, visualDuration: 0.25, bounce: 0.08 },
  screen: { duration: 0.15 },
};

type Destination = "workers" | "projects" | "prs";
type Icon = typeof Bot;
type ScreenProps = {
  navigationTrigger: React.RefObject<HTMLButtonElement | null>;
  onOpenNavigation: () => void;
};
type Worker = {
  title: string; project: string; branch: string; status: string;
  color: string; icon: string; time?: string; pr?: string; chip?: boolean;
};

const destinations: Array<{ id: Destination; label: string; icon: Icon; badge?: number }> = [
  { id: "workers", label: "Workers", icon: Bot, badge: 1 },
  { id: "projects", label: "Projects", icon: Folder },
  { id: "prs", label: "Pull Requests", icon: GitPullRequest, badge: 3 },
];

const workerSections: Array<{ label: string; color: string; rows: Worker[] }> = [
  { label: "Needs you", color: t.amber, rows: [{
    title: "auth migration", project: "agent-orchestrator-mo",
    branch: "ao/agent-orchestrator-mo-16/root", status: "Needs input",
    color: t.amber, icon: "/app-icons/coverage-claude-code.svg",
  }] },
  { label: "Working", color: t.orange, rows: [
    { title: "mobile revamp", project: "agent-orchestrator-mo", branch: "ao/agent-orchestrator-mo-17/root", status: "Working", color: t.orange, time: "1m", icon: "/app-icons/coverage-claude-code.svg" },
    { title: "push notifications", project: "meetyou", branch: "ao/meetyou-9/root", status: "Working", color: t.orange, time: "12m", icon: "/app-icons/coverage-cursor.svg", chip: true },
  ] },
  { label: "Ready", color: t.green, rows: [{
    title: "fix pin and unpin", project: "meetyou", branch: "ao/meetyou-6/pin-fix",
    status: "Ready to merge", color: t.green, pr: "PR #4 · CI passing",
    icon: "/app-icons/coverage-codex.svg",
  }] },
];

const projects = [
  { name: "agent-orchestrator-mo", status: "Running", detail: "1 needs you · 3 working", workers: "12 workers", color: t.green },
  { name: "meetyou", status: "Idle", detail: "1 ready · 1 working", workers: "6 workers", color: t.tertiary },
  { name: "adorable", status: "Running", detail: "2 working", workers: "4 workers", color: t.green },
];

const pullRequests = [
  { number: 5660, repo: "aoagents/agent-orchestrator", title: "feat: separate task effort selection", meta: "main · 7 checks passing", color: t.green },
  { number: 4, repo: "Prasad-D-Ware/meetyou", title: "fix: restore pin state after refresh", meta: "main · ready to merge", color: t.green },
  { number: 31, repo: "Zerith-Studio/adorable", title: "feat: tighten onboarding copy", meta: "main · review requested", color: t.purple },
];

export function MobileAppDemo() {
  const reduceMotion = Boolean(useReducedMotion());
  const [destination, setDestination] = useState<Destination>("workers");
  const [navigationOpen, setNavigationOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const navigationTrigger = useRef<HTMLButtonElement>(null);

  const closeNavigation = () => {
    setNavigationOpen(false);
    requestAnimationFrame(() => navigationTrigger.current?.focus());
  };

  const selectDestination = (next: Destination) => {
    setDestination(next);
    setNavigationOpen(false);
    setSearchOpen(false);
  };

  return (
    <div className="relative flex h-[300px] w-full items-center justify-center sm:h-[380px] lg:h-[420px]">
      <div aria-hidden="true" className="pointer-events-none absolute size-[260px] rounded-full bg-[#4d8dff] opacity-[0.13] blur-[70px]" />
      <div className="relative shrink-0 origin-center scale-[0.481] sm:scale-[0.609] lg:scale-[0.673]" style={{ width: FRAME_W, height: FRAME_H }}>
        <PhoneFrame>
          <StatusBar />
          <div className="relative min-h-0 flex-1 overflow-hidden">
            <AnimatePresence mode="wait" initial={false}>
              <motion.div
                key={destination}
                initial={reduceMotion ? false : { opacity: 0, x: 8 }}
                animate={{ opacity: 1, x: 0 }}
                exit={reduceMotion ? undefined : { opacity: 0, x: -8 }}
                transition={reduceMotion ? { duration: 0 } : MOTION.screen}
                aria-hidden={navigationOpen}
                inert={navigationOpen}
                className="absolute inset-0 flex flex-col"
              >
                {destination === "workers" ? (
                  <WorkersScreen navigationTrigger={navigationTrigger} onOpenNavigation={() => setNavigationOpen(true)} searchOpen={searchOpen} onToggleSearch={() => setSearchOpen((open) => !open)} reduceMotion={reduceMotion} />
                ) : destination === "projects" ? (
                  <ProjectsScreen navigationTrigger={navigationTrigger} onOpenNavigation={() => setNavigationOpen(true)} />
                ) : (
                  <PullRequestsScreen navigationTrigger={navigationTrigger} onOpenNavigation={() => setNavigationOpen(true)} />
                )}
              </motion.div>
            </AnimatePresence>
            <AnimatePresence>
              {navigationOpen ? <NavigationDrawer active={destination} reduceMotion={reduceMotion} onClose={closeNavigation} onSelect={selectDestination} /> : null}
            </AnimatePresence>
          </div>
        </PhoneFrame>
      </div>
    </div>
  );
}

function PhoneFrame({ children }: { children: React.ReactNode }) {
  return (
    <div className="relative h-full w-full">
      <span aria-hidden="true" className="absolute -left-[2px] top-[96px] h-[22px] w-[3px] rounded-l-[2px] bg-gradient-to-r from-[#575c66] to-[#22252a]" />
      <span aria-hidden="true" className="absolute -left-[2px] top-[142px] h-[38px] w-[3px] rounded-l-[2px] bg-gradient-to-r from-[#575c66] to-[#22252a]" />
      <span aria-hidden="true" className="absolute -left-[2px] top-[192px] h-[38px] w-[3px] rounded-l-[2px] bg-gradient-to-r from-[#575c66] to-[#22252a]" />
      <span aria-hidden="true" className="absolute -right-[2px] top-[168px] h-[58px] w-[3px] rounded-r-[2px] bg-gradient-to-l from-[#575c66] to-[#22252a]" />
      <div className="relative h-full w-full rounded-[46px] p-[9px] shadow-[0_1px_1px_rgba(255,255,255,0.16)_inset,0_-1px_1px_rgba(255,255,255,0.06)_inset,0_36px_70px_-22px_rgba(0,0,0,0.85)]" style={{ background: "linear-gradient(152deg,#63686f 0%,#2a2d33 22%,#16181c 52%,#23262b 78%,#4c5158 100%)" }}>
        <div className="relative flex h-full w-full flex-col overflow-hidden rounded-[38px] font-sans antialiased" style={{ backgroundColor: t.bgBase, color: t.text }}>
          {children}
          <span aria-hidden="true" className="pointer-events-none absolute left-1/2 top-[9px] z-50 h-[24px] w-[84px] -translate-x-1/2 rounded-full bg-black ring-1 ring-white/[0.04]" />
          <span aria-hidden="true" className="pointer-events-none absolute bottom-[7px] left-1/2 z-50 h-[4px] w-[100px] -translate-x-1/2 rounded-full bg-white/35" />
          <span aria-hidden="true" className="pointer-events-none absolute -left-[40%] -top-[10%] z-40 h-[130%] w-[70%] rotate-[18deg] bg-gradient-to-r from-transparent via-white/[0.045] to-transparent" />
          <span aria-hidden="true" className="pointer-events-none absolute inset-0 z-50 rounded-[38px] ring-1 ring-inset ring-white/[0.07]" />
        </div>
      </div>
    </div>
  );
}

function StatusBar() {
  return (
    <div className="relative z-10 flex h-[34px] shrink-0 items-center justify-between px-[22px] pt-[4px]">
      <span className="text-[11px] font-semibold tabular-nums">9:41</span>
      <div className="flex items-center gap-[5px]" aria-hidden="true">
        <span className="flex items-end gap-[1.5px]">{[3, 5, 7, 9].map((height) => <i key={height} className="w-[2px] rounded-[1px] bg-white" style={{ height }} />)}</span>
        <Wifi className="size-[11px]" strokeWidth={2.4} />
        <span className="flex h-[10px] w-[19px] items-center rounded-[3px] border border-white/45 p-[1.5px]"><i className="block h-full w-[75%] rounded-[1px] bg-white" /></span>
      </div>
    </div>
  );
}

function ScreenHeader({ title, navigationTrigger, onOpenNavigation }: ScreenProps & { title: string }) {
  return (
    <header className="flex h-[48px] shrink-0 items-center gap-[8px] border-b px-[10px]" style={{ borderColor: t.borderSubtle }}>
      <RoundButton ref={navigationTrigger} label="Open navigation" icon={Menu} onClick={onOpenNavigation} />
      <h4 className="min-w-0 flex-1 truncate text-[20px] font-extrabold tracking-[-0.5px]">{title}</h4>
      <span className="relative block size-[40px]">
        <RoundButton label="Notifications" icon={Bell} />
        <span className="pointer-events-none absolute right-[3px] top-[2px] size-[7px] rounded-full ring-2 ring-[#0a0b0d]" style={{ backgroundColor: t.blue }} />
      </span>
    </header>
  );
}

function WorkersScreen({ navigationTrigger, onOpenNavigation, searchOpen, onToggleSearch, reduceMotion }: ScreenProps & { searchOpen: boolean; onToggleSearch: () => void; reduceMotion: boolean }) {
  return (
    <div className="relative flex h-full min-h-0 flex-col">
      <ScreenHeader title="Workers" navigationTrigger={navigationTrigger} onOpenNavigation={onOpenNavigation} />
      <div className="relative min-h-0 flex-1 overflow-hidden">
        {workerSections.map((section) => <section key={section.label}>
          <SectionHeader label={section.label} count={section.rows.length} color={section.color} />
          {section.rows.map((worker) => <WorkerRow key={`${worker.project}:${worker.title}`} worker={worker} />)}
        </section>)}
        <BottomFade />
      </div>
      <div className="absolute inset-x-[14px] bottom-[14px] z-10">
        <AnimatePresence initial={false}>{searchOpen ? <motion.div initial={reduceMotion ? false : { opacity: 0, y: 4 }} animate={{ opacity: 1, y: 0 }} exit={reduceMotion ? undefined : { opacity: 0, y: 4 }} transition={reduceMotion ? { duration: 0 } : MOTION.screen} className="mb-[7px] flex h-[44px] items-center gap-[7px] rounded-[22px] border px-[8px] pl-[12px] shadow-lg backdrop-blur-xl" style={{ backgroundColor: "rgba(18,19,23,0.68)", borderColor: "rgba(255,255,255,0.15)" }}>
          <Search className="size-[14px]" style={{ color: t.tertiary }} /><span className="flex-1 text-[11px]" style={{ color: t.tertiary }}>Search workers</span>
          <RoundButton label="Close search" icon={X} onClick={onToggleSearch} />
        </motion.div> : null}</AnimatePresence>
        <div role="toolbar" aria-label="Worker actions" className="flex h-[40px] items-center justify-between">
          <div className="flex items-center gap-[6px]">
            <RoundButton label="Worker filters" icon={SlidersHorizontal} />
            <RoundButton label="Search workers" icon={Search} onClick={onToggleSearch} active={searchOpen} />
          </div>
          <button type="button" aria-label="New worker" className="grid size-[40px] place-items-center rounded-full border outline-none motion-safe:transition-transform motion-safe:duration-100 active:scale-[0.94] focus-visible:ring-2 focus-visible:ring-white" style={{ background: "linear-gradient(145deg,rgba(255,255,255,0.96),rgba(225,229,237,0.78))", borderColor: "rgba(255,255,255,0.8)", color: t.bgBase, boxShadow: "0 7px 18px rgba(0,0,0,0.38),0 1px 0 rgba(255,255,255,0.9) inset" }}><Plus className="size-[17px]" strokeWidth={2.2} /></button>
        </div>
      </div>
    </div>
  );
}

function ProjectsScreen(props: ScreenProps) {
  return <div className="flex h-full min-h-0 flex-col"><ScreenHeader title="Projects" {...props} /><div className="relative min-h-0 flex-1 overflow-hidden"><SectionHeader label="All projects" count={projects.length} color={t.blue} />{projects.map((project) => <ProjectRow key={project.name} project={project} />)}<BottomFade /></div></div>;
}

function PullRequestsScreen(props: ScreenProps) {
  return <div className="flex h-full min-h-0 flex-col"><ScreenHeader title="Pull Requests" {...props} /><div className="relative min-h-0 flex-1 overflow-hidden"><SectionHeader label="Open" count={pullRequests.length} color={t.green} />{pullRequests.map((pr) => <PullRequestRow key={pr.number} pullRequest={pr} />)}<BottomFade /></div></div>;
}

function NavigationDrawer({ active, reduceMotion, onClose, onSelect }: { active: Destination; reduceMotion: boolean; onClose: () => void; onSelect: (next: Destination) => void }) {
  const recent = workerSections.slice(0, 2).map((section) => section.rows[0]);
  const drawer = useRef<HTMLElement>(null);
  const closeButton = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    closeButton.current?.focus();

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== "Tab" || !drawer.current) return;

      const focusable = [...drawer.current.querySelectorAll<HTMLElement>(
        'button:not([disabled]), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
      )];
      const first = focusable[0];
      const last = focusable.at(-1);
      if (!first || !last) return;

      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  return (
    <div role="dialog" aria-modal="true" aria-label="Navigation menu" className="absolute inset-0 z-30">
      <motion.button type="button" aria-label="Close navigation" aria-hidden="true" tabIndex={-1} onClick={onClose} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: reduceMotion ? 0 : 0.15 }} className="absolute inset-0 cursor-default bg-black/65 outline-none" />
      <motion.nav ref={drawer} aria-label="Mobile app" initial={reduceMotion ? false : { x: "-100%" }} animate={{ x: 0 }} exit={reduceMotion ? undefined : { x: "-100%" }} transition={reduceMotion ? { duration: 0 } : MOTION.drawer} className="absolute inset-y-0 left-0 flex w-[218px] flex-col border-r px-[12px] pb-[16px] pt-[12px] shadow-[18px_0_40px_rgba(0,0,0,0.45)]" style={{ backgroundColor: t.bgSurface, borderColor: t.border }}>
        <div className="flex items-center gap-[9px] px-[7px] pb-[12px] pt-[4px]">
          <span className="relative grid size-[43px] place-items-center rounded-[13px]" style={{ backgroundColor: t.bgElevated }}><img src="/ao-logo.svg" alt="" className="size-[34px]" /><Dot color={t.green} className="absolute bottom-[3px] right-[3px] ring-2 ring-[#121317]" /></span>
          <span className="min-w-0 flex-1"><b className="block truncate text-[12px]">Agent Orchestrator</b><span className="text-[9px] font-semibold" style={{ color: t.green }}>Connected</span></span>
          <RoundButton ref={closeButton} label="Close navigation" icon={X} onClick={onClose} />
        </div>
        <div className="space-y-[3px]">{destinations.map((item) => {
          const SelectedIcon = item.icon; const selected = item.id === active;
          return <button key={item.id} type="button" aria-current={selected ? "page" : undefined} onClick={() => onSelect(item.id)} className="flex h-[42px] w-full items-center gap-[10px] rounded-[10px] px-[10px] text-left text-[12px] font-semibold outline-none focus-visible:ring-2 focus-visible:ring-[#4d8dff]" style={{ backgroundColor: selected ? t.bgSubtle : "transparent", color: selected ? t.text : t.secondary }}><SelectedIcon className="size-[16px]" style={{ color: selected ? t.blue : t.tertiary }} /><span className="flex-1">{item.label}</span>{item.badge ? <span className="rounded-full px-[6px] py-[2px] font-mono text-[9px]" style={{ backgroundColor: t.bgSubtle, color: selected ? t.blue : t.tertiary }}>{item.badge}</span> : null}</button>;
        })}</div>
        <p className="px-[10px] pb-[5px] pt-[17px] text-[9px] font-bold uppercase tracking-[1.1px]" style={{ color: t.faint }}>Recent workers</p>
        {recent.map((worker) => <button key={worker.title} type="button" className="flex h-[42px] items-center gap-[9px] rounded-[9px] px-[10px] text-left outline-none focus-visible:ring-2 focus-visible:ring-[#4d8dff]"><HarnessMark worker={worker} size={15} /><span className="min-w-0 flex-1"><span className="block truncate text-[11px] font-semibold">{worker.title}</span><span className="block truncate text-[9px]" style={{ color: t.tertiary }}>{worker.project}</span></span><Dot color={worker.color} /></button>)}
        <div className="mt-auto border-t pt-[8px]" style={{ borderColor: t.borderSubtle }}><button type="button" className="flex h-[42px] w-full items-center gap-[10px] rounded-[10px] px-[10px] text-[12px] font-semibold outline-none focus-visible:ring-2 focus-visible:ring-[#4d8dff]" style={{ color: t.secondary }}><Settings className="size-[16px]" />Settings</button></div>
      </motion.nav>
    </div>
  );
}

function SectionHeader({ label, count, color }: { label: string; count: number; color: string }) {
  return <div className="flex items-center gap-[7px] px-[16px] pb-[7px] pt-[13px]"><span className="h-[10px] w-[2.5px] rounded-full" style={{ backgroundColor: color }} /><span className="flex-1 text-[9px] font-bold uppercase tracking-[1.1px]" style={{ color: t.secondary }}>{label}</span><span className="font-mono text-[9px] font-bold" style={{ color: t.tertiary }}>{count}</span></div>;
}

function WorkerRow({ worker }: { worker: Worker }) {
  return <button type="button" className="block min-h-[76px] w-full border-b px-[16px] py-[9px] text-left outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[#4d8dff]" style={{ borderColor: t.borderSubtle }}><span className="flex items-center gap-[6px]"><HarnessMark worker={worker} size={14} /><span className="min-w-0 flex-1 truncate text-[9.5px]" style={{ color: t.secondary }}>{worker.project}</span><CircleDot className="size-[10px]" style={{ color: worker.color }} /><span className="text-[9px] font-semibold" style={{ color: worker.color }}>{worker.time ?? worker.status}</span></span><span className="mt-[3px] block truncate text-[12.5px] font-semibold">{worker.title}</span><span className="mt-[3px] flex items-center gap-[4px] font-mono text-[8.5px]" style={{ color: t.tertiary }}><GitBranch className="size-[9px]" /><span className="min-w-0 flex-1 truncate">{worker.branch}</span>{worker.pr ? <span style={{ color: t.green }}>{worker.pr}</span> : null}</span></button>;
}

function ProjectRow({ project }: { project: (typeof projects)[number] }) {
  return <div className="relative border-b" style={{ borderColor: t.borderSubtle }}><button type="button" className="block w-full px-[16px] py-[12px] pr-[112px] text-left outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[#4d8dff]"><span className="flex items-center"><b className="min-w-0 flex-1 truncate text-[12.5px]">{project.name}</b><ChevronRight className="size-[13px]" style={{ color: t.faint }} /></span><span className="mt-[5px] flex items-center gap-[5px] text-[9.5px]"><Dot color={project.color} /><b style={{ color: project.color }}>{project.status}</b><span className="truncate" style={{ color: t.tertiary }}>· {project.detail}</span></span><span className="mt-[10px] block text-[9.5px]" style={{ color: t.tertiary }}>{project.workers}</span></button><button type="button" aria-label={`Open orchestrator for ${project.name}`} className="absolute bottom-[12px] right-[16px] h-[30px] rounded-full border px-[12px] text-[9.5px] font-semibold outline-none backdrop-blur-xl motion-safe:transition-transform motion-safe:duration-100 active:scale-[0.96] focus-visible:ring-2 focus-visible:ring-[#4d8dff]" style={{ background: "linear-gradient(145deg,rgba(255,255,255,0.92),rgba(220,225,234,0.72))", borderColor: "rgba(255,255,255,0.72)", color: t.bgBase, boxShadow: "0 6px 14px rgba(0,0,0,0.34),0 1px 0 rgba(255,255,255,0.9) inset" }}>Orchestrator</button></div>;
}

function PullRequestRow({ pullRequest }: { pullRequest: (typeof pullRequests)[number] }) {
  return <button type="button" className="block min-h-[92px] w-full border-b px-[16px] py-[11px] text-left outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[#4d8dff]" style={{ borderColor: t.borderSubtle }}><span className="flex items-center gap-[6px]"><GitPullRequest className="size-[12px]" style={{ color: pullRequest.color }} /><b className="font-mono text-[9.5px]" style={{ color: pullRequest.color }}>#{pullRequest.number}</b><span className="min-w-0 flex-1 truncate text-right font-mono text-[8.5px]" style={{ color: t.tertiary }}>{pullRequest.repo}</span></span><b className="mt-[6px] block text-[11.5px] leading-[15px]">{pullRequest.title}</b><span className="mt-[6px] flex items-center gap-[5px] text-[9px]" style={{ color: t.tertiary }}><Check className="size-[10px]" style={{ color: pullRequest.color }} />{pullRequest.meta}</span></button>;
}

function HarnessMark({ worker, size }: { worker: Worker; size: number }) {
  return <span className="grid shrink-0 place-items-center rounded-[4px]" style={{ width: size, height: size, backgroundColor: worker.chip ? "#24272e" : "transparent" }}><img src={worker.icon} alt="" draggable="false" style={{ width: worker.chip ? size - 4 : size, height: worker.chip ? size - 4 : size }} /></span>;
}

function Dot({ color, className = "" }: { color: string; className?: string }) {
  return <span className={`size-[6px] shrink-0 rounded-full ${className}`} style={{ backgroundColor: color }} />;
}

function BottomFade() {
  return <span aria-hidden="true" className="pointer-events-none absolute inset-x-0 bottom-0 h-[78px]" style={{ background: `linear-gradient(to top, ${t.bgBase} 14%, rgba(10,11,13,0))` }} />;
}

function RoundButton({ icon: ButtonIcon, label, onClick, active, ref }: { icon: Icon; label: string; onClick?: () => void; active?: boolean; ref?: React.Ref<HTMLButtonElement> }) {
  return <button ref={ref} type="button" aria-label={label} onClick={onClick} className="grid size-[40px] shrink-0 place-items-center rounded-full border outline-none backdrop-blur-xl motion-safe:transition-transform motion-safe:duration-100 active:scale-[0.93] focus-visible:ring-2 focus-visible:ring-[#4d8dff]" style={{ color: active ? t.blue : t.secondary, background: active ? "linear-gradient(145deg,rgba(77,141,255,0.22),rgba(255,255,255,0.08))" : "linear-gradient(145deg,rgba(255,255,255,0.13),rgba(255,255,255,0.045))", borderColor: active ? "rgba(77,141,255,0.48)" : "rgba(255,255,255,0.16)", boxShadow: "0 5px 14px rgba(0,0,0,0.28),0 1px 0 rgba(255,255,255,0.16) inset,-1px -1px 0 rgba(0,0,0,0.18) inset" }}><ButtonIcon className="size-[17px]" strokeWidth={1.8} /></button>;
}
