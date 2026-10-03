import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { source } from "@/lib/source";
import { baseOptions } from "@/lib/layout.shared";
import type { ReactNode } from "react";
import { visibleProductName } from "@/lib/branding";

type Tree = ReturnType<typeof source.getPageTree>;
type Node = Tree["children"][number];

function displayDescription(description: ReactNode): ReactNode {
  return typeof description === "string" ? visibleProductName(description) : description;
}

function displayNode(node: Node): Node {
  if (node.type === "separator") return node;
  if (node.type === "page") return { ...node, description: displayDescription(node.description) };
  return {
    ...node,
    description: displayDescription(node.description),
    index: node.index && { ...node.index, description: displayDescription(node.index.description) },
    children: node.children.map(displayNode),
  };
}

function displayTree(tree: Tree): Tree {
  return {
    ...tree,
    description: displayDescription(tree.description),
    children: tree.children.map(displayNode),
    fallback: tree.fallback && displayTree(tree.fallback),
  };
}

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <DocsLayout tree={displayTree(source.getPageTree())} {...baseOptions()}>
      {children}
    </DocsLayout>
  );
}
