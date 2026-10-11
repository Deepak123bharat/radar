import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { groupsOf, project, tool } from "../evidenceFixtures";

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

function projectCase(name: string) {
  const fixture = cases.find((item) => item.name === name);
  if (!fixture) throw new Error(`no fixture case ${name}`);
  return project([
    tool(
      "list",
      "list_resources",
      fixture.result.content.map((block) => block.text).join(""),
      { summary: JSON.stringify(fixture.arguments) },
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
      "Radar found no Services in namespaces alpha, gamma; other namespaces were not read, so this result cannot show that none exist elsewhere.",
    ]);
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
    expect(groupsOf(complete.groups, "inventory")[0].latest.summary).toBe(
      "1 returned",
    );
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
