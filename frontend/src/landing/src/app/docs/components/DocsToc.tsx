"use client";

import { useEffect, useState } from "react";
import type { TocItem } from "@/lib/docs";

export function DocsToc({ toc }: { toc: TocItem[] }) {
  const [active, setActive] = useState("");

  useEffect(() => {
    const ids = toc.map((t) => t.id);
    const update = () => {
      // If scrolled to the bottom of the document, activate the last heading
      const isAtBottom =
        window.innerHeight + Math.ceil(window.scrollY) >=
        document.documentElement.scrollHeight - 60;

      if (isAtBottom && ids.length > 0) {
        setActive(ids[ids.length - 1]);
        return;
      }

      let current = ids[0] ?? "";
      for (const id of ids) {
        const el = document.getElementById(id);
        // Last heading whose top has scrolled past the header band is the active one.
        if (el && el.getBoundingClientRect().top <= 120) current = id;
      }
      setActive(current);
    };
    update();
    window.addEventListener("scroll", update, { passive: true });
    return () => window.removeEventListener("scroll", update);
  }, [toc]);

  if (toc.length === 0) return null;

  return (
    <aside className="scrollbar-hide sticky top-20 hidden h-[calc(100vh-6rem)] w-52 shrink-0 overflow-y-auto py-10 2xl:block">
      <div className="mb-3 text-sm font-medium text-muted-foreground">On this page</div>
      <ul className="flex flex-col gap-1.5 border-l border-border/60">
        {toc.map((item) => (
          <li key={item.id}>
            <a
              href={`#${item.id}`}
              className={`-ml-px block border-l text-sm transition-colors ${item.level === 3 ? "pl-6" : "pl-3"} ${
                active === item.id
                  ? "border-foreground/70 text-foreground"
                  : "border-transparent text-muted-foreground hover:border-foreground/30 hover:text-foreground"
              }`}
            >
              {item.text}
            </a>
          </li>
        ))}
      </ul>
    </aside>
  );
}
