import { afterEach, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";
import { GET } from "./route";

afterEach(() => vi.unstubAllEnvs());
it("publishes only a service DID matching the actual host", async () => {
  vi.stubEnv("ATPROTO_SERVICE_DID", "did:web:mycontextprotocol.dev#mycontextprotocol");
  const response = GET(new NextRequest("https://mycontextprotocol.dev/.well-known/did.json"));
  expect(response.status).toBe(200);
  expect(await response.json()).toEqual({ "@context": ["https://www.w3.org/ns/did/v1"], id: "did:web:mycontextprotocol.dev", service: [{ id: "#mycontextprotocol", type: "MyContextProtocol", serviceEndpoint: "https://mycontextprotocol.dev" }] });
  expect(GET(new NextRequest("https://preview.example/.well-known/did.json")).status).toBe(503);
});
