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
it("publishes and verifies from one click without requiring review pagination", async () => {
  vi.mocked(fetchPublicationReview).mockResolvedValue({ ...review, next_cursor: 25 });
  vi.mocked(publishProjectReview).mockResolvedValueOnce({ status: "pending", digest: review.digest, completed: 1, total: 2 }).mockResolvedValueOnce({ status: "complete", digest: review.digest, completed: 2, total: 2 });
  await click("Publish records");
  expect(fetchPublicationReview).toHaveBeenCalledExactlyOnceWith("project", 0, expect.any(AbortSignal), false);
  expect(publishProjectReview).toHaveBeenCalledTimes(2);
  expect(publishProjectReview).toHaveBeenCalledWith("project", review.digest, false, expect.any(AbortSignal));
  expect(host.textContent).toContain("complete (2/2)");
  expect(verifyProjectRecords).toHaveBeenCalledTimes(1);
  expect(host.textContent).toContain("ATProto migration complete");
  expect(host.textContent).not.toContain("Load next review page");
});
it("continues all pending batches without an arbitrary request limit", async () => {
  let batches = 0;
  vi.mocked(publishProjectReview).mockImplementation(async () => ({ status: ++batches < 24 ? "pending" : "complete", digest: review.digest }));
  await click("Publish records");
  expect(publishProjectReview).toHaveBeenCalledTimes(24);
  expect(host.textContent).toContain("ATProto migration complete");
});
it("stops if publication returns a different digest", async () => {
  vi.mocked(publishProjectReview).mockResolvedValue({ status: "pending", digest: "changed" });
  await click("Publish records");
  expect(publishProjectReview).toHaveBeenCalledTimes(1);
  expect(verifyProjectRecords).not.toHaveBeenCalled();
  expect(host.textContent).toContain("Project data changed");
});
it("retains the digest on a failed request and resumes without loading a new review", async () => {
  vi.mocked(publishProjectReview).mockResolvedValueOnce({ status: "pending", digest: review.digest, completed: 1, total: 2 }).mockRejectedValueOnce(new Error("Offline"));
  await click("Publish records");
  expect(host.textContent).toContain("The operation could not finish");
  vi.mocked(publishProjectReview).mockResolvedValueOnce({ status: "complete", digest: review.digest, completed: 2, total: 2 });
  await click("Continue publication");
  expect(fetchPublicationReview).toHaveBeenCalledTimes(1);
  expect(publishProjectReview).toHaveBeenLastCalledWith("project", review.digest, false, expect.any(AbortSignal));
  expect(host.textContent).toContain("ATProto migration complete");
});
it("does not publish when preparing the publication fails", async () => {
  vi.mocked(fetchPublicationReview).mockRejectedValueOnce(new Error("Offline"));
  await click("Publish records");
  expect(publishProjectReview).not.toHaveBeenCalled();
  expect(host.textContent).toContain("The operation could not finish");
});
it("pauses publication, ignores late responses and resumes the same digest", async () => {
  let finish!: (result: { status: string; digest: string }) => void;
  vi.mocked(publishProjectReview).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  await click("Publish records");
  await click("Pause publication");
  expect(vi.mocked(publishProjectReview).mock.calls[0][3]?.aborted).toBe(true);
  await act(async () => finish({ status: "complete", digest: review.digest }));
  expect(verifyProjectRecords).not.toHaveBeenCalled();
  expect(host.textContent).not.toContain("ATProto migration complete");
  vi.mocked(publishProjectReview).mockResolvedValueOnce({ status: "complete", digest: review.digest });
  await click("Continue publication");
  expect(fetchPublicationReview).toHaveBeenCalledTimes(1);
  expect(host.textContent).toContain("ATProto migration complete");
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

it("focuses publishing after a migration without reading or writing records automatically", async () => {
  await act(async () => root.render(<QueryClientProvider client={client}><PublicationReviewSection projectId="project" reviewRequest={1} /></QueryClientProvider>));
  expect(fetchPublicationReview).not.toHaveBeenCalled();
  expect(publishProjectReview).not.toHaveBeenCalled();
  expect(document.activeElement?.textContent).toBe("Publish records");
});
it("includes public telemetry only after an explicit advanced opt-in", async () => {
  vi.mocked(fetchPublicationReview).mockResolvedValue({ ...review, has_telemetry: true });
  vi.mocked(publishProjectReview).mockResolvedValue({ status: "complete", digest: review.digest });
  await act(async () => host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
  await click("Publish records");
  expect(fetchPublicationReview).toHaveBeenCalledWith("project", 0, expect.any(AbortSignal), true);
  expect(publishProjectReview).toHaveBeenCalledWith("project", review.digest, true, expect.any(AbortSignal));
});
it("does not publish an unexpected telemetry-inclusive metadata response", async () => {
  vi.mocked(fetchPublicationReview).mockResolvedValue({ ...review, has_telemetry: true });
  await click("Publish records");
  expect(publishProjectReview).not.toHaveBeenCalled();
});
it("does not publish a review belonging to another project", async () => {
  vi.mocked(fetchPublicationReview).mockResolvedValue({ ...review, project_id: "another-project" });
  await click("Publish records");
  expect(publishProjectReview).not.toHaveBeenCalled();
});
it("does not claim migration is complete when post-publication verification fails", async () => {
  vi.mocked(publishProjectReview).mockResolvedValue({ status: "complete", digest: review.digest });
  vi.mocked(verifyProjectRecords).mockResolvedValue({ ...verification, status: "missing" });
  await click("Publish records");
  expect(host.textContent).toContain("Publication finished, but verification has not passed");
  expect(host.textContent).not.toContain("ATProto migration complete");
});
