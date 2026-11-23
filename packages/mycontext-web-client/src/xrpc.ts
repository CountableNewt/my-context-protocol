import { XRPC_HTTP_METHODS, XRPC_NO_OUTPUT, type XRPCMethods } from "./xrpc-types";

export type XRPCMethod = keyof XRPCMethods;

/**
 * The caller supplies method-scoped service authorization for this service DID
 * and NSID. PDS OAuth access tokens belong at the PDS, never at these endpoints.
 */
export interface XRPCTransport {
  fetch(request: Request): Promise<Response>;
}

export class XRPCError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly retryAfterSeconds?: number,
  ) {
    super(message);
    this.name = "XRPCError";
  }
}

export class XRPCClient {
  private readonly origin: string;

  constructor(origin: string, private readonly transport: XRPCTransport) {
    const url = new URL(origin);
    if (
      (url.protocol !== "https:" && !(url.protocol === "http:" && ["127.0.0.1", "localhost", "[::1]"].includes(url.hostname))) ||
      url.username || url.password || url.search || url.hash || (url.pathname !== "/" && url.pathname !== "")
    ) throw new Error("XRPC requires an HTTPS service origin or a local development origin");
    this.origin = url.origin;
  }

  async call<M extends XRPCMethod>(method: M, input: XRPCMethods[M]["input"], signal?: AbortSignal): Promise<XRPCMethods[M]["output"]> {
    if (!Object.prototype.hasOwnProperty.call(XRPC_HTTP_METHODS, method)) throw new Error("Unknown XRPC method");
    const httpMethod = XRPC_HTTP_METHODS[method];
    const url = new URL(`/xrpc/${method}`, this.origin);
    if (httpMethod === "GET") {
      for (const [key, value] of Object.entries(input)) {
        if (value === undefined) continue;
        if (!["string", "number", "boolean"].includes(typeof value)) throw new Error("XRPC query arguments must be scalar values");
        url.searchParams.set(key, String(value));
      }
    }
    const request = new Request(url, {
      method: httpMethod,
      headers: httpMethod === "POST" ? { accept: "application/json", "content-type": "application/json" } : { accept: "application/json" },
      body: httpMethod === "POST" ? JSON.stringify(input) : undefined,
      credentials: "omit",
      redirect: "error",
      cache: "no-store",
      signal,
    });
    const response = await this.transport.fetch(request);
    if (response.status === 204) {
      if (!XRPC_NO_OUTPUT[method]) throw new XRPCError(204, "InvalidResponse", "XRPC service omitted a required result");
      return undefined as XRPCMethods[M]["output"];
    }
    const raw = await boundedText(response, 4 * 1024 * 1024);
    let value: unknown;
    try { value = JSON.parse(raw); } catch { throw new XRPCError(response.status, "InvalidResponse", "XRPC service returned invalid JSON"); }
    if (!response.ok) {
      const error = value && typeof value === "object" ? value as Record<string, unknown> : {};
      const retry = Number(response.headers.get("retry-after"));
      throw new XRPCError(response.status, typeof error.error === "string" ? error.error : "ServiceUnavailable", typeof error.message === "string" ? error.message : "XRPC request failed", Number.isSafeInteger(retry) && retry > 0 ? retry : undefined);
    }
    return value as XRPCMethods[M]["output"];
  }
}

async function boundedText(response: Response, limit: number): Promise<string> {
  if (!response.body) throw new XRPCError(response.status, "InvalidResponse", "XRPC service returned no response body");
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let count = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      count += value.byteLength;
      if (count > limit) {
        await reader.cancel();
        throw new XRPCError(response.status, "InvalidResponse", "XRPC response exceeds the size limit");
      }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const bytes = new Uint8Array(count);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
  return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
}
