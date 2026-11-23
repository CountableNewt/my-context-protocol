import { NextRequest, NextResponse } from "next/server";

/** Publishes service location only; user and OAuth private keys are never included. */
export function GET(request: NextRequest) {
  const configured = process.env.ATPROTO_SERVICE_DID?.trim();
  if (!configured) return NextResponse.json({ error: "Not found" }, { status: 404 });
  const [did, fragment, extra] = configured.split("#");
  const origin = new URL(request.url).origin;
  const expected = `did:web:${encodeURIComponent(new URL(origin).host)}`;
  if (extra !== undefined || !fragment || did !== expected || !/^[A-Za-z0-9_-]+$/.test(fragment)) {
    return NextResponse.json({ error: "Invalid service identity configuration" }, { status: 503 });
  }
  return NextResponse.json({
    "@context": ["https://www.w3.org/ns/did/v1"],
    id: did,
    service: [{ id: `#${fragment}`, type: "MyContextProtocol", serviceEndpoint: origin }],
  }, { headers: { "cache-control": "public, max-age=300" } });
}
