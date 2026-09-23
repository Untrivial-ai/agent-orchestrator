import { COMPANY } from "@ao/shared/constants";

export interface NavLink {
  href: string;
  label: string;
  description?: string;
  external?: boolean;
}

export const PRODUCT_LINKS: NavLink[] = [
  {
    href: "/#see-it",
    label: "Demo",
    description: "Watch AO run a fleet of agents end to end.",
  },
  {
    href: "/design-partners",
    label: "Design Partners",
    description: "Build the future of multi-agent development with AO.",
  },
];

export const RESOURCE_LINKS: NavLink[] = [
  {
    href: "/docs",
    label: "Documentation",
    description: "Guides, references, and integrations.",
  },
  {
    href: "/compare",
    label: "Compare",
    description: "AO versus Superset, Conductor, Paseo, and peers.",
  },
  {
    href: COMPANY.GITHUB_URL,
    label: "GitHub",
    description: "Open source under Apache 2.0.",
    external: true,
  },
];

export const TOP_LEVEL_LINKS: NavLink[] = [];
