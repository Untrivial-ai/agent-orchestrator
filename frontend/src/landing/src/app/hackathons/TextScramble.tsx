"use client";

import { useEffect, useRef } from "react";

const characters = "abcdefghijklmnopqrstuvwxyz0123456789";

function randomCharacter() {
  return characters[Math.floor(Math.random() * characters.length)];
}

export function TextScramble({ text }: { text: string }) {
  const textRef = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

    const element = textRef.current;
    const staticLayer = element?.querySelector<HTMLElement>("[data-scramble-static]");
    const overlay = element?.querySelector<HTMLElement>("[data-scramble-overlay]");
    if (!element || !staticLayer || !overlay) return;

    let timer: number | undefined;
    let cancelled = false;

    const start = () => {
      if (cancelled) return;

      const overlayRect = overlay.getBoundingClientRect();
      const fontSize = Number.parseFloat(window.getComputedStyle(element).fontSize);
      const words = Array.from(
        staticLayer.querySelectorAll<HTMLElement>("[data-scramble-word]"),
      ).map((word) => {
        const rect = word.getBoundingClientRect();
        const scrambledWord = document.createElement("span");
        scrambledWord.style.position = "absolute";
        scrambledWord.style.left = `${rect.left - overlayRect.left}px`;
        scrambledWord.style.top = `${rect.top - overlayRect.top}px`;
        scrambledWord.style.whiteSpace = "nowrap";
        scrambledWord.style.lineHeight = "inherit";

        return {
          original: word.textContent ?? "",
          sourceRect: rect,
          element: scrambledWord,
        };
      });

      overlay.replaceChildren(...words.map((word) => word.element));
      words.forEach((word) => {
        const overlayWordRect = word.element.getBoundingClientRect();
        const verticalOffset =
          word.sourceRect.top - overlayWordRect.top + fontSize * 0.125;
        word.element.style.transform = `translateY(${verticalOffset}px)`;
      });
      staticLayer.style.color = "transparent";

      let frame = 0;
      const totalFrames = 18;
      timer = window.setInterval(() => {
        frame += 1;

        words.forEach((word) => {
          const resolvedCharacters = Math.floor(
            (frame / totalFrames) * word.original.length,
          );

          word.element.textContent = Array.from(word.original, (character, index) => {
            if (!/[a-z]/i.test(character) || index < resolvedCharacters) {
              return character;
            }
            return randomCharacter();
          }).join("");
        });

        if (frame >= totalFrames) {
          staticLayer.style.color = "";
          overlay.replaceChildren();
          window.clearInterval(timer);
        }
      }, 40);
    };

    if (document.fonts.status === "loaded") {
      start();
    } else {
      void document.fonts.ready.then(start);
    }

    return () => {
      cancelled = true;
      if (timer !== undefined) window.clearInterval(timer);
    };
  }, [text]);

  return (
    <span ref={textRef} aria-label={text}>
      <span data-scramble-static>
        {text.split(/(\s+)/).map((part, index) =>
          /\s/.test(part) ? (
            part
          ) : (
            <span key={`${part}-${index}`} data-scramble-word>
              {part}
            </span>
          ),
        )}
      </span>
      <span
        data-scramble-overlay
        aria-hidden="true"
        className="pointer-events-none absolute inset-0"
      />
    </span>
  );
}
