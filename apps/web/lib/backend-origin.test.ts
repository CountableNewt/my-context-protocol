import { afterEach, describe, expect, it, vi } from "vitest";
import { getBackendOrigin } from "./backend-origin";

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("getBackendOrigin", () => {
  it("uses the requested deployment binding before legacy origins", () => {
    vi.stubEnv("IDENTITY_SERVICE_URL", "https://identity.internal/");
    vi.stubEnv("CONTROL_SERVICE_URL", "https://control.internal/");
    vi.stubEnv("SYNC_SERVICE_URL", "https://sync.internal/");
    vi.stubEnv("BACKEND_URL", "https://old.example");
    expect(getBackendOrigin()).toBe("https://identity.internal");
    expect(getBackendOrigin("control")).toBe("https://control.internal");
    expect(getBackendOrigin("sync")).toBe("https://sync.internal");
  });

  it("fails closed if a preview binding is missing instead of reaching legacy data", () => {
    vi.stubEnv("VERCEL_SERVICE_MODE", "1");
    vi.stubEnv("CONTROL_SERVICE_URL", "");
    vi.stubEnv("BACKEND_URL", "https://production.example");
    expect(() => getBackendOrigin("control")).toThrow("Missing control service binding");
  });
  it("prefers the server-only backend URL and removes a trailing slash", () => {
    vi.stubEnv("BACKEND_URL", "http://gateway.railway.internal:8080/");
    vi.stubEnv("API_ORIGIN", "https://api-origin.example");
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://public-api.example");

    expect(getBackendOrigin()).toBe("http://gateway.railway.internal:8080");
  });

  it("falls back through API_ORIGIN, the public URL, and localhost", () => {
    vi.stubEnv("BACKEND_URL", "");
    vi.stubEnv("API_ORIGIN", "https://api-origin.example/");
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://public-api.example/");
    expect(getBackendOrigin()).toBe("https://api-origin.example");

    vi.stubEnv("API_ORIGIN", "");
    expect(getBackendOrigin()).toBe("https://public-api.example");

    vi.stubEnv("NEXT_PUBLIC_API_URL", "");
    expect(getBackendOrigin()).toBe("http://localhost:8080");
  });
});
