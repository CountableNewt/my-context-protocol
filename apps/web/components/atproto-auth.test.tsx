import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ATProtoAuth } from "./atproto-auth";

describe("ATProtoAuth", () => {
  it("renders no authorization action when the server has not enabled it", () => {
    expect(renderToStaticMarkup(<ATProtoAuth enabled={false} />)).toBe("");
  });
  it("renders an accessible handle form and disables empty submission", () => {
    const html = renderToStaticMarkup(<ATProtoAuth enabled />);
    expect(html).toContain('aria-label="Sign in with ATProto"');
    expect(html).toContain('autoComplete="username"');
    expect(html).toContain("ATProto handle");
    expect(html).toContain("disabled");
  });
  it("separates account linking from public data consent", () => {
    const html = renderToStaticMarkup(<ATProtoAuth linking enabled returnTo="/account" />);
    expect(html).toContain("Link ATProto Account");
    expect(html).toContain("Linking alone does not publish project data");
  });
});
