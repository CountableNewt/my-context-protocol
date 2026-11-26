/** @vitest-environment jsdom */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { PublicationReviewSection } from "./publication-review-section";
import { fetchPublicationReview, publishProjectReview, verifyProjectRecords, type PublicationVerification, reconcileProjectRecords, fetchPublicationConflict, discardPublicationConflict } from "@/lib/atproto-projects-api";
vi.mock("@/lib/atproto-projects-api", () => ({ fetchPublicationReview: vi.fn(), publishProjectReview: vi.fn(), verifyProjectRecords: vi.fn(), reconcileProjectRecords: vi.fn(), fetchPublicationConflict: vi.fn(), discardPublicationConflict: vi.fn() }));
let host: HTMLDivElement, root: Root, client: QueryClient;
const review = { owner_did: "did:plc:owner", project_id: "project", digest: "reviewed-digest", entry_count: 30, has_telemetry: false, warnings: ["Review public metadata"], entries_json: '[{"table":"compiled_skills"}]' };
beforeEach(async () => { host=document.createElement("div"); document.body.append(host);root=createRoot(host);client=new QueryClient();vi.mocked(fetchPublicationReview).mockResolvedValue(review);vi.mocked(verifyProjectRecords).mockResolvedValue(verification);await act(async()=>root.render(<QueryClientProvider client={client}><PublicationReviewSection projectId="project" /></QueryClientProvider>)); });
afterEach(async()=>{await act(async()=>root.unmount());host.remove();client.clear();vi.resetAllMocks();});
async function click(text:string) {const button=[...host.querySelectorAll("button")].find(b=>b.textContent===text);expect(button).toBeDefined();await act(async()=>button!.click());}
it("requires every metadata review page and renewed public consent before publishing and verification", async()=>{
 vi.mocked(fetchPublicationReview).mockResolvedValueOnce({...review,next_cursor:25}).mockResolvedValueOnce(review);
 vi.mocked(publishProjectReview).mockResolvedValueOnce({status:"pending",digest:review.digest,completed:1,total:2}).mockResolvedValueOnce({status:"complete",digest:review.digest,completed:2,total:2});
 await click("Review publication");expect(host.querySelectorAll('input[type="checkbox"]')).toHaveLength(1);
 await click("Load next review page");const inputs=host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]');expect(inputs).toHaveLength(2);expect(inputs[0].checked).toBe(false);expect(inputs[1].checked).toBe(false);
 const publish=[...host.querySelectorAll("button")].find(b=>b.textContent==="Publish reviewed records");expect(publish?.disabled).toBe(true);expect(publishProjectReview).not.toHaveBeenCalled();
 await act(async()=>inputs[1].click());await click("Publish reviewed records");expect(publishProjectReview).toHaveBeenCalledTimes(2);expect(publishProjectReview).toHaveBeenCalledWith("project",review.digest,false,expect.any(AbortSignal));expect(host.textContent).toContain("complete (2/2)");expect(verifyProjectRecords).toHaveBeenCalledTimes(1);expect(host.textContent).toContain("ATProto migration complete");
 await click("Review publication");expect(host.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(false);
});
it("invalidates stale review when a later page has a changed digest",async()=>{
 vi.mocked(fetchPublicationReview).mockResolvedValueOnce({...review,next_cursor:25}).mockResolvedValueOnce({...review,digest:"changed"});
 await click("Review publication");await click("Load next review page");expect(host.textContent).toContain("Project data changed during review");expect(host.querySelectorAll('input[type="checkbox"]')).toHaveLength(1);expect(publishProjectReview).not.toHaveBeenCalled();
});
it("keeps bounded pending publication resumable under the same explicit consent",async()=>{
 vi.mocked(publishProjectReview).mockResolvedValue({status:"pending",digest:review.digest});await click("Review publication");await act(async()=>host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')[1].click());await click("Publish reviewed records");expect(publishProjectReview).toHaveBeenCalledTimes(20);vi.mocked(publishProjectReview).mockResolvedValueOnce({status:"complete",digest:review.digest});await click("Continue publication");expect(publishProjectReview).toHaveBeenCalledTimes(21);
});
it("offers owner reconciliation without granting publication consent",async()=>{vi.mocked(reconcileProjectRecords).mockResolvedValue({status:"complete"});await click("Reconcile owner records");expect(reconcileProjectRecords).toHaveBeenCalledWith("project",expect.any(AbortSignal));expect(publishProjectReview).not.toHaveBeenCalled();});

it("requires separate explicit conflict review before CID-bound discard",async()=>{vi.mocked(fetchPublicationConflict).mockResolvedValue({status:"superseded",intent_id:"intent",current_manifest_cid:"current-cid",source_cid:"prior-cid",target_digest:"pending-digest"});vi.mocked(discardPublicationConflict).mockResolvedValue({status:"complete"});await click("Inspect publication conflict");const button=[...host.querySelectorAll("button")].find(b=>b.textContent==="Discard reviewed pending mutation");expect(button?.disabled).toBe(true);expect(discardPublicationConflict).not.toHaveBeenCalled();await act(async()=>host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')[1].click());await click("Discard reviewed pending mutation");expect(discardPublicationConflict).toHaveBeenCalledWith("project","intent","current-cid",expect.any(AbortSignal));});

const verification: PublicationVerification = { status: "verified", owner_did: "did:plc:owner", project_id: "project", checked_records: 3, collections: [{ collection: "dev.mycontextprotocol.data.frontmatter", checked_records: 1 }], issues: [] };
it("verifies owner records and shows per-collection results without publication or reconciliation", async () => {
  vi.mocked(verifyProjectRecords).mockResolvedValue(verification);
  await click("Verify records");
  expect(verifyProjectRecords).toHaveBeenCalledWith("project", expect.any(AbortSignal));
  expect(host.textContent).toContain("All published records verified");
  expect(host.textContent).toContain("3 records checked");
  expect(host.textContent).toContain("dev.mycontextprotocol.data.frontmatter");
  expect(publishProjectReview).not.toHaveBeenCalled();
  expect(reconcileProjectRecords).not.toHaveBeenCalled();
  expect(host.querySelectorAll('input[type="checkbox"]')).toHaveLength(1);
  expect(host.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(false);
});
it.each([
  ["not_published", "Project records are not published"],
  ["missing", "Published records are missing"],
  ["invalid", "Published records do not match"],
  ["out_of_sync", "Published metadata is out of sync"],
  ["unavailable", "Verification unavailable"],
  ["incomplete", "Verification incomplete"],
] as const)("does not claim a successful verification for %s", async (status, title) => {
  vi.mocked(verifyProjectRecords).mockResolvedValue({ ...verification, status, checked_records: 0, collections: [], issues: [{ code: "RecordCheck", message: "Check could not confirm this record.", uri: "at://did:plc:owner/dev.mycontextprotocol.data.frontmatter/key", expected_cid: "expected-cid" }] });
  await click("Verify records");
  expect(host.textContent).toContain(title);
  expect(host.textContent).not.toContain("All published records verified");
  expect(host.textContent).toContain("at://did:plc:owner/dev.mycontextprotocol.data.frontmatter/key");
  expect(publishProjectReview).not.toHaveBeenCalled();
});
it("cancels a verification and ignores its late success response", async () => {
  let finish!: (result: PublicationVerification) => void;
  vi.mocked(verifyProjectRecords).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
  await click("Verify records");
  expect([...host.querySelectorAll("button")].find((button) => button.textContent === "Verifying records…")?.disabled).toBe(true);
  await click("Cancel verification");
  expect(vi.mocked(verifyProjectRecords).mock.calls[0][1]?.aborted).toBe(true);
  await act(async () => finish(verification));
  expect(host.textContent).toContain("Verification canceled");
  expect(host.textContent).not.toContain("All published records verified");
  expect(publishProjectReview).not.toHaveBeenCalled();
});
it("clears a prior result if a retry fails", async () => {
  vi.mocked(verifyProjectRecords).mockResolvedValueOnce(verification).mockRejectedValueOnce(new Error("Offline"));
  await click("Verify records");
  await click("Verify records");
  expect(host.textContent).not.toContain("All published records verified");
  expect(host.textContent).toContain("The operation could not finish");
});

it("rejects a verification result belonging to a different project", async () => {
  vi.mocked(verifyProjectRecords).mockResolvedValue({ ...verification, project_id: "another-project" });
  await click("Verify records");
  expect(host.textContent).not.toContain("All published records verified");
  expect(host.textContent).toContain("The operation could not finish");
});

it("opens metadata-only review when the migration requests it without granting publication consent", async () => {
  await act(async () => root.render(<QueryClientProvider client={client}><PublicationReviewSection projectId="project" reviewRequest={1} /></QueryClientProvider>));
  expect(fetchPublicationReview).toHaveBeenCalledWith("project", 0, expect.any(AbortSignal), false);
  expect(host.textContent).toContain("30 records for");
  expect(publishProjectReview).not.toHaveBeenCalled();
  expect([...host.querySelectorAll("button")].find((b) => b.textContent === "Publish reviewed records")?.disabled).toBe(true);
});
it("requires both a telemetry-inclusive review and separate telemetry consent", async () => {
  vi.mocked(fetchPublicationReview).mockResolvedValue({ ...review, has_telemetry: true });
  vi.mocked(publishProjectReview).mockResolvedValue({ status: "complete", digest: review.digest });
  await act(async () => host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
  await click("Review publication");
  expect(fetchPublicationReview).toHaveBeenCalledWith("project", 0, expect.any(AbortSignal), true);
  const inputs = host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]');
  expect(inputs).toHaveLength(3);
  await act(async () => inputs[1].click());
  expect([...host.querySelectorAll("button")].find((b) => b.textContent === "Publish reviewed records")?.disabled).toBe(true);
  expect(publishProjectReview).not.toHaveBeenCalled();
  await act(async () => inputs[2].click());
  await click("Publish reviewed records");
  expect(publishProjectReview).toHaveBeenCalledWith("project", review.digest, true, expect.any(AbortSignal));
});
it("does not claim migration is complete when post-publication verification fails", async () => {
  vi.mocked(publishProjectReview).mockResolvedValue({ status: "complete", digest: review.digest });
  vi.mocked(verifyProjectRecords).mockResolvedValue({ ...verification, status: "missing" });
  await click("Review publication");
  await act(async () => host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')[1].click());
  await click("Publish reviewed records");
  expect(host.textContent).toContain("Publication finished, but verification has not passed");
  expect(host.textContent).not.toContain("ATProto migration complete");
});
