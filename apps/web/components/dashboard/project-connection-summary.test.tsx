/** @vitest-environment jsdom */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Project } from "@/lib/types";
import { ProjectConnectionSummary } from "./project-connection-summary";

const project: Project = { id: "p", account_id: "a", name: "Skills", slug: "skills", subdomain: "skills", created_at: "2026-11-25" };

describe("ProjectConnectionSummary", () => {
  it.each([["github", "GitHub"], ["tangled", "ATProto · Tangled"], ["future-provider", "future-provider"]])("identifies %s and displays repository and branch", (provider, label) => {
    const markup = renderToStaticMarkup(<ProjectConnectionSummary project={{ ...project, repo_connection: { provider, repo_owner: "did:plc:owner", repo_name: "skills", default_branch: "release" } }} />);
    expect(markup).toContain(label);
    expect(markup).toContain("did:plc:owner/skills");
    expect(markup).toContain("Branch: release");
    expect(markup).not.toContain("<a"); // Cards wrap this summary in their project link.
  });
  it("distinguishes disconnected projects from unavailable connection data", () => {
    expect(renderToStaticMarkup(<ProjectConnectionSummary project={{ ...project, repo_connection: null }} />)).toContain("No repository connected");
    expect(renderToStaticMarkup(<ProjectConnectionSummary project={project} />)).toContain("Repository connection unavailable");
  });
  it("uses the Tangled handle for display without changing the source owner DID", () => {
    const connection = { provider: "tangled", repo_owner: "did:plc:owner", owner_handle: "samclemente.me", repo_name: "skills", default_branch: "main" };
    const markup = renderToStaticMarkup(<ProjectConnectionSummary project={{ ...project, repo_connection: connection }} />);
    expect(markup).toContain("samclemente.me/skills");
    expect(markup).not.toContain("did:plc:owner");
    expect(connection.repo_owner).toBe("did:plc:owner");
  });

});
