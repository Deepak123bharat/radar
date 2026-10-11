import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { DiagnosisEvidenceItem } from "../../../../api/diagnose";
import { InvestigationEvidencePane } from "../../InvestigationEvidencePane";
import { resolveInvestigationCase } from "../../investigationCase";
import { evidenceRef, groupsOf, project, tool } from "../evidenceFixtures";

// The list_resources results a partial read returns over MCP. internal/mcp
// pins them to the producer and internal/ai proves every agent backend hands
// their joined text blocks to Findings unchanged; this suite projects that
// text the way Findings does.
const FIXTURE = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "../../../../../../testdata/mcp/list-resources-partial-coverage.json",
);

interface FixtureCase {
  name: string;
  arguments: Record<string, unknown>;
  result: { content: { type: string; text: string }[] };
}

const cases: FixtureCase[] = JSON.parse(readFileSync(FIXTURE, "utf8")).cases;

function projectCase(name: string, ref?: string) {
  const fixture = cases.find((item) => item.name === name);
  if (!fixture) throw new Error(`no fixture case ${name}`);
  return project([
    tool(
      "list",
      "list_resources",
      fixture.result.content.map((block) => block.text).join(""),
      { summary: JSON.stringify(fixture.arguments), evidenceRef: ref },
    ),
  ]);
}

describe("a list Radar's collector read only in some namespaces", () => {
  it("shows the rows it read with the namespaces they came from", () => {
    const projection = projectCase("partial");
    const [inventory] = groupsOf(projection.groups, "inventory");
    if (inventory?.latest.data.type !== "inventory") {
      throw new Error("expected an inventory card");
    }
    expect(
      inventory.latest.data.resources.map((row) => [row.namespace, row.name]),
    ).toEqual([["alpha", "alpha-object"]]);
    expect(inventory.latest.summary).toBe(
      "1 returned · only alpha, gamma read",
    );
    expect(inventory.latest.data.namespacesRead).toEqual(["alpha", "gamma"]);
    expect(projection.limitations).toEqual([
      expect.objectContaining({
        source: "Resource inventory",
        kind: "unknown",
        message:
          "Radar read Services only in namespaces alpha, gamma; other namespaces were not read, so this list cannot show that none exist elsewhere.",
      }),
    ]);
    expect(projection.limitations[0].sources[0].id).toBe(
      inventory.latest.source.id,
    );
  });

  it("does not present an empty partial read as an empty cluster", () => {
    const projection = projectCase("partial and empty");
    expect(groupsOf(projection.groups, "inventory")).toEqual([]);
    expect(projection.limitations.map((item) => item.message)).toEqual([
      'The listing returned nothing. Radar read services only in namespaces "alpha,gamma"; objects in other namespaces were not read, so this result cannot show they don\'t exist.',
    ]);
  });

  it("names the resource Radar read, not the alias the agent asked for", () => {
    const projection = projectCase("partial and empty, kind by alias");
    const [message] = projection.limitations.map((item) => item.message);
    expect(message).toContain("Radar read services only in");
    expect(message).not.toMatch(/svcs/i);
  });

  it("says a cited name's namespace was not read instead of calling it absent", () => {
    const ref = evidenceRef("a", "b");
    const projection = projectCase("partial", ref);
    const evidence: DiagnosisEvidenceItem[] = [
      { namespace: "beta", claim: "Service api is missing from beta." },
      { namespace: "gamma", claim: "Service api is missing from gamma." },
      { namespace: undefined, claim: "No Service api exists." },
    ].map(({ namespace, claim }) => ({
      status: "linked",
      ref,
      role: "cause",
      claim,
      subject: { kind: "Service", namespace, name: "api" },
    }));
    const investigationCase = resolveInvestigationCase(
      projection,
      { evidence },
      0,
    );
    expect(investigationCase.items.map((item) => item.placement)).toEqual([
      "card",
      "card",
      "card",
    ]);
    const html = renderToStaticMarkup(
      createElement(InvestigationEvidencePane, {
        projection,
        investigationCase,
        story: {
          report: "The Ingress has no backend.\n\n[[radar:evidence=0]]",
          evidence,
        },
        collecting: false,
        animateGroupIds: new Set<string>(),
        onViewSource: () => {},
        onViewActivity: () => {},
      }),
    );
    const start = html.indexOf("data-story-inventory-rows");
    expect(start).toBeGreaterThan(-1);
    const block = html.slice(start, html.indexOf('-body"', start));
    // beta was never read, so the listing cannot say api is missing there.
    expect(block).toContain("beta/api: beta was not read");
    expect(block).not.toContain("Not in this listing: beta/api");
    // gamma was read and holds nothing: there the listing does show absence.
    expect(block).toContain("Not in this listing: gamma/api");
    // An unqualified name was looked for only where the listing read.
    expect(block).toContain("api: not in the namespaces read (alpha, gamma)");
    expect(block).not.toContain("Not in this listing: api<");
  });

  it("keeps a complete list's rows and copy unchanged", () => {
    const complete = project([
      tool(
        "list",
        "list_resources",
        [{ kind: "Service", name: "alpha-object", namespace: "alpha" }],
        { summary: JSON.stringify({ kind: "services" }) },
      ),
    ]);
    const [inventory] = groupsOf(complete.groups, "inventory");
    expect(inventory.latest.summary).toBe("1 returned");
    expect(inventory.latest.data).not.toHaveProperty("namespacesRead");
    expect(complete.limitations).toEqual([]);
    const empty = project([
      tool("list", "list_resources", [], {
        summary: JSON.stringify({ kind: "services" }),
      }),
    ]);
    expect(empty.limitations.map((item) => item.message)).toEqual([
      "Radar found no matching resources for services resources. Anything you do not have permission to read was not searched.",
    ]);
  });

  it("does not guess at a coverage object it cannot read", () => {
    for (const payload of [
      { items: [], coverage: { complete: false } },
      { items: [], coverage: { complete: true, namespaces: ["alpha"] } },
      { items: {}, coverage: { complete: false, namespaces: ["alpha"] } },
    ]) {
      const projection = project([
        tool("list", "list_resources", payload, {
          summary: JSON.stringify({ kind: "services" }),
        }),
      ]);
      expect(projection.groups).toEqual([]);
      expect(projection.limitations.map((item) => item.message)).toEqual([
        "Radar couldn't summarize this investigation step. Review it in Activity.",
      ]);
    }
  });
});
