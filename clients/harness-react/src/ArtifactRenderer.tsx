// ArtifactRenderer — renders a generative-UI Artifact via a host-supplied
// component catalog. Apps register their own components keyed by name, so
// the SDK never imports application UI: a healthcare app registers
// <PatientChart>, a fintech app registers <PortfolioCard>, etc.

import type {
  Artifact,
  ComponentArtifact,
  FileArtifact,
} from "@harness/client";
import type { ComponentType, ReactElement } from "react";
import { createElement } from "react";

export interface ComponentCatalogEntry {
  /** React component that accepts `props` from the wire artifact. */
  component: ComponentType<Record<string, unknown>>;
  /**
   * Optional runtime validator. Returning false blocks rendering — useful
   * with `zod.safeParse(props).success`. Defaults to allow-all.
   */
  validate?: (props: unknown) => boolean;
}

export type ComponentCatalog = Record<string, ComponentCatalogEntry>;

export interface ArtifactRendererProps {
  artifact: Artifact;
  catalog: ComponentCatalog;
  /** Optional renderer for file artifacts. Defaults to a <pre> dump. */
  fileRenderer?: (file: FileArtifact) => ReactElement;
  /** Rendered when a component name is unknown to the catalog. */
  fallback?: (artifact: Artifact, reason: string) => ReactElement;
  /**
   * Invoked when an interactive component (one whose Artifact has an
   * `interaction` field) submits user input. The host is expected to POST
   * the data to /api/interrupts/:token/resolve. When omitted, interactive
   * components still render but their onSubmit prop is a no-op.
   */
  onInteractionSubmit?: (
    interaction: { token: string; chat_id?: number },
    data: unknown,
  ) => void | Promise<void>;
}

export function ArtifactRenderer({
  artifact,
  catalog,
  fileRenderer,
  fallback,
  onInteractionSubmit,
}: ArtifactRendererProps): ReactElement | null {
  if (artifact.kind === "file" && artifact.file) {
    return fileRenderer
      ? fileRenderer(artifact.file)
      : createElement(
          "pre",
          null,
          (artifact.file as FileArtifact).content ?? "",
        );
  }

  if (artifact.kind === "component" && artifact.component) {
    const comp = artifact.component as ComponentArtifact;
    const entry = catalog[comp.name];
    if (!entry) {
      return fallback
        ? fallback(artifact, `unknown component: ${comp.name}`)
        : createElement(
            "div",
            { role: "alert" },
            `Unknown component "${comp.name}"`,
          );
    }
    const baseProps = (comp.props ?? {}) as Record<string, unknown>;
    if (entry.validate && !entry.validate(baseProps)) {
      return fallback
        ? fallback(artifact, `invalid props for ${comp.name}`)
        : createElement(
            "div",
            { role: "alert" },
            `Invalid props for "${comp.name}"`,
          );
    }
    // Inject onSubmit when the artifact carries an interaction token so the
    // catalog component can return user input to the paused agent loop.
    const interaction = (
      artifact as { interaction?: { token: string; chat_id?: number } }
    ).interaction;
    const props = interaction
      ? {
          ...baseProps,
          onSubmit: (data: unknown) => {
            if (onInteractionSubmit) {
              void onInteractionSubmit(interaction, data);
            }
          },
        }
      : baseProps;
    return createElement(entry.component, props);
  }

  return null;
}
