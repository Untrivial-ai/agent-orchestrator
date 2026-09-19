"use client";

import { AnimatePresence, motion } from "motion/react";
import { useState } from "react";
import { HiPlus } from "react-icons/hi2";
import { HashLink } from "../HashLink/HashLink";
import type { FAQItem } from "./constants";
import { FAQ_ITEMS } from "./constants";

function FAQAccordionItem({
  item,
  isOpen,
  onToggle,
}: {
  item: FAQItem;
  isOpen: boolean;
  onToggle: () => void;
}) {
  return (
    <div>
      <button
        type="button"
        onClick={onToggle}
        className="group flex w-full items-center justify-between py-6 text-left transition-all outline-none"
      >
        <span className="text-base sm:text-lg font-medium text-foreground pr-4">
          {item.question}
        </span>
        <HiPlus
          className={`h-5 w-5 shrink-0 text-muted-foreground transition-transform duration-200 ${
            isOpen ? "rotate-45" : ""
          }`}
        />
      </button>
      <AnimatePresence initial={false}>
        {isOpen && (
          <motion.div
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: "auto", opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={{ duration: 0.2, ease: "easeInOut" }}
            className="overflow-hidden"
          >
            <div className="space-y-3 pb-6 pr-12">
              <p className="text-base text-muted-foreground leading-relaxed">
                {item.answer}
              </p>
              {item.related ? (
                <p className="text-sm">
                  <HashLink
                    href={item.related.href}
                    className="text-muted-foreground underline-offset-4 transition-colors hover:text-foreground hover:underline"
                  >
                    {item.related.label}
                  </HashLink>
                </p>
              ) : null}
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
}

export function FAQSection() {
  const [openIndex, setOpenIndex] = useState<number | null>(null);

  const handleToggle = (index: number) => {
    setOpenIndex(openIndex === index ? null : index);
  };

  return (
    <section id="faq" className="relative px-4 py-16 sm:px-8 sm:py-20 lg:px-[30px] lg:py-24">
      <div className="max-w-7xl mx-auto">
        <div className="grid grid-cols-1 xl:grid-cols-[1fr_1.5fr] gap-12 xl:gap-20">
          <div className="xl:sticky xl:top-24 xl:self-start">
            <h2 className="text-3xl sm:text-4xl xl:text-5xl font-medium tracking-[-0.5px] text-foreground leading-[1.1]">
              Frequently
              <br />
              asked questions
            </h2>
          </div>

          <div>
            <div className="w-full divide-y divide-border">
              {FAQ_ITEMS.map((item, index) => (
                <FAQAccordionItem
                  key={item.question}
                  item={item}
                  isOpen={openIndex === index}
                  onToggle={() => handleToggle(index)}
                />
              ))}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
