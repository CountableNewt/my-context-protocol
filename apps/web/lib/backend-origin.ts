export type BackendService = "identity" | "control" | "sync";

/** Runtime bindings keep server requests in the current branch deployment. */
export function getBackendOrigin(service: BackendService = "identity"): string {
  const binding = {
    identity: process.env.IDENTITY_SERVICE_URL,
    control: process.env.CONTROL_SERVICE_URL,
    sync: process.env.SYNC_SERVICE_URL,
  }[service]?.trim();
  if (binding) return binding.replace(/\/$/, "");
  if (process.env.VERCEL_SERVICE_MODE === "1") {
    throw new Error(`Missing ${service} service binding`);
  }
  const raw =
    process.env.BACKEND_URL?.trim() ||
    process.env.API_ORIGIN?.trim() ||
    process.env.NEXT_PUBLIC_API_URL?.trim() ||
    "http://localhost:8080";
  return raw.replace(/\/$/, "");
}
