/** @vitest-environment jsdom */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import LandingPage from "./page";
import { MAIN_CONTENT_ID } from "@/lib/a11y";

describe("public landing page", () => {
  it("explains the product without a session, an auth provider, or backend data", () => {
    const document = new DOMParser().parseFromString(
      renderToStaticMarkup(<LandingPage />),
      "text/html",
    );

    expect(document.querySelectorAll("h1")).toHaveLength(1);
    expect(document.querySelector(`main#${MAIN_CONTENT_ID}`)).not.toBeNull();
    expect(document.querySelector("main")?.textContent).toContain("hosted MCP endpoint");
    expect(document.querySelector("main")?.textContent).toContain("compatible AI tool");
    // The example remains readable text for visitors and assistive technology.
    expect(document.querySelector("figure[aria-label]")?.textContent).toContain("SKILL.md");
  });

  it("offers sign-in and onboarding through the existing login flow", () => {
    const document = new DOMParser().parseFromString(
      renderToStaticMarkup(<LandingPage />),
      "text/html",
    );
    const links = Array.from(document.querySelectorAll("a"));
    for (const label of ["Sign in", "Get started"]) {
      expect(links.find((link) => link.textContent?.trim() === label)?.getAttribute("href"))
        .toBe("/login");
    }
    expect(links.find((link) => link.textContent?.includes("Explore the source"))?.getAttribute("href"))
      .toBe("https://github.com/CountableNewt/my-context-protocol");
  });
});
