import { apiFetch } from "@/lib/api";

export interface TangledRepository { repository_did: string; owner_did: string; owner_handle?: string; name: string; default_branch: string; uri: string; cid: string }
export interface TangledRepositories { provider: "tangled"; repositories: TangledRepository[]; cursor?: string; private_repository_selection: string }
export function fetchTangledRepositories(cursor?: string) {
  return apiFetch<TangledRepositories>(`/repositories/tangled${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`);
}
export interface PublicationReview { owner_did: string; project_id: string; digest: string; entry_count: number; has_telemetry: boolean; warnings: string[]; entries_json: string; next_cursor?: number }
export interface PublicationProgress { status: string; digest: string; completed?: number; total?: number; uri?: string; cid?: string }
const publicationPath = (id: string) => `/projects/${encodeURIComponent(id)}/atproto/publication`;
export function fetchPublicationReview(id: string, cursor = 0, signal?: AbortSignal, includeTelemetry = false) {
  return apiFetch<PublicationReview>(`${publicationPath(id)}?cursor=${cursor}&limit=25&include_telemetry=${includeTelemetry}`, { signal });
}
export function publishProjectReview(id: string, digest: string, telemetry: boolean, signal?: AbortSignal) {
  return apiFetch<PublicationProgress>(publicationPath(id), { method: "POST", body: JSON.stringify({ review_digest: digest, public: true, telemetry }), signal });
}
export function reconcileProjectRecords(id: string, signal?: AbortSignal) {
  return apiFetch<{ status: string }>(`/projects/${encodeURIComponent(id)}/atproto/reconcile`, { method: "POST", body: "{}", signal });
}
export interface PublicationConflict { status: string; intent_id?: string; source_cid?: string; target_digest?: string; current_manifest_cid?: string; current_digest?: string }
export function fetchPublicationConflict(id: string, signal?: AbortSignal) { return apiFetch<PublicationConflict>(`/projects/${encodeURIComponent(id)}/atproto/conflict`, { signal }); }
export function discardPublicationConflict(id: string, intentId: string, currentCID: string, signal?: AbortSignal) { return apiFetch<{ status: string }>(`/projects/${encodeURIComponent(id)}/atproto/conflict`, { method: "POST", body: JSON.stringify({ intent_id: intentId, expected_current_cid: currentCID, action: "discard" }), signal }); }

export interface PublicationVerification {
  status: "verified" | "not_published" | "missing" | "invalid" | "out_of_sync" | "unavailable" | "incomplete";
  owner_did: string;
  project_id: string;
  checked_records: number;
  collections: Array<{ collection: string; checked_records: number }>;
  issues: Array<{ code: string; message: string; uri?: string; expected_cid?: string; actual_cid?: string }>;
}
export function verifyProjectRecords(id: string, signal?: AbortSignal) {
  return apiFetch<PublicationVerification>(`/projects/${encodeURIComponent(id)}/atproto/verify`, { method: "POST", body: "{}", signal });
}
