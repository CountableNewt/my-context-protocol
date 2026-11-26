"use client";

import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { fetchPublicationReview, publishProjectReview, verifyProjectRecords, type PublicationVerification, reconcileProjectRecords, fetchPublicationConflict, discardPublicationConflict, type PublicationConflict, type PublicationProgress, type PublicationReview } from "@/lib/atproto-projects-api";
import { PublicationVerificationResult } from "./publication-verification-result";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export function PublicationReviewSection({ projectId }: { projectId: string }) {
  const [conflict, setConflict] = useState<PublicationConflict | null>(null);
  const [discardConsent, setDiscardConsent] = useState(false);
  const [review, setReview] = useState<PublicationReview | null>(null);
  const [pages, setPages] = useState<string[]>([]);
  const [consent, setConsent] = useState(false);
  const [telemetry, setTelemetry] = useState(false);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<PublicationProgress | null>(null);
  const [verification, setVerification] = useState<PublicationVerification | null>(null);
  const [verifying, setVerifying] = useState(false);
  const [message, setMessage] = useState("");
  const request = useRef<AbortController | null>(null);
  const client = useQueryClient();
  useEffect(() => () => request.current?.abort(), [projectId]);
  function begin() { request.current?.abort(); const next = new AbortController(); request.current = next; setBusy(true); setMessage(""); setVerification(null); return next; }
  function report(error: unknown, signal: AbortSignal) { if (!signal.aborted) setMessage(error instanceof ApiError ? formatApiErrorDetail(error.body) || error.message : "The operation could not finish. You can retry safely."); }
  async function verify() {
    const action = begin();
    setVerifying(true);
    try {
      const result = await verifyProjectRecords(projectId, action.signal);
      if (!action.signal.aborted) {
        if (result.project_id !== projectId) throw new Error("Verification returned a different project");
        setVerification(result);
      }
    } catch (error) { report(error, action.signal); }
    finally {
      if (request.current === action) { setVerifying(false); if (!action.signal.aborted) setBusy(false); }
    }
  }
  async function load(more = false) {
    const action = begin();
    try {
      const next = await fetchPublicationReview(projectId, more ? review?.next_cursor : 0, action.signal);
      if (action.signal.aborted) return;
      if (more && review && next.digest !== review.digest) {
        setReview(null); setPages([]); setConsent(false); setTelemetry(false); setProgress(null); setMessage("Project data changed during review. Load a fresh review before consenting."); return;
      }
      setReview(next); setPages((prior) => more ? [...prior, next.entries_json] : [next.entries_json]);
      if (!more) { setConsent(false); setTelemetry(false); setProgress(null); }
    } catch (error) { report(error, action.signal); }
    finally { if (!action.signal.aborted) setBusy(false); }
  }
  async function publish() {
    if (!review || !consent || review.next_cursor !== undefined) return;
    const action = begin();
    try {
      // Each bounded response confirms canonical progress. Continue with the
      // same reviewed digest and consent; interrupted requests remain resumable.
      for (let i = 0; i < 20 && !action.signal.aborted; i++) {
        const next = await publishProjectReview(projectId, review.digest, telemetry, action.signal);
        if (action.signal.aborted) return;
        setProgress(next);
        if (next.digest !== review.digest) { setConsent(false); setMessage("The publication digest changed. Load a fresh review before continuing."); return; }
        if (next.status !== "pending") { void client.invalidateQueries({ queryKey: ["project", projectId] }); return; }
      }
    } catch (error) { if (error instanceof ApiError && error.status === 409) { setConsent(false); setReview(null); setPages([]); } report(error, action.signal); }
    finally { if (!action.signal.aborted) setBusy(false); }
  }
  async function inspectConflict() {
    const action = begin();
    try { const next = await fetchPublicationConflict(projectId, action.signal); if (!action.signal.aborted) { setConflict(next); setDiscardConsent(false); } }
    catch (error) { report(error, action.signal); }
    finally { if (!action.signal.aborted) setBusy(false); }
  }
  async function discardConflict() {
    if (conflict?.status !== "superseded" || !conflict.intent_id || !conflict.current_manifest_cid || !discardConsent) return;
    const action = begin();
    try {
      const next = await discardPublicationConflict(projectId, conflict.intent_id, conflict.current_manifest_cid, action.signal);
      if (!action.signal.aborted) { setMessage(`Conflict resolution: ${next.status}. Reload the publication review before writing again.`); setConflict(null); setDiscardConsent(false); setConsent(false); setReview(null); setPages([]); }
    } catch (error) { setDiscardConsent(false); report(error, action.signal); }
    finally { if (!action.signal.aborted) setBusy(false); }
  }
  async function reconcile() {
    const action = begin();
    try { const next = await reconcileProjectRecords(projectId, action.signal); if (!action.signal.aborted) { setMessage(`Reconciliation: ${next.status}.`); void client.invalidateQueries(); } }
    catch (error) { report(error, action.signal); }
    finally { if (!action.signal.aborted) setBusy(false); }
  }
  return <Card><CardHeader><CardTitle>ATProto records</CardTitle><CardDescription>Review and authorize publication of durable project data to your account’s PDS.</CardDescription></CardHeader>
    <CardContent className="space-y-4">
      <p className="text-sm">These records are public. Review metadata and feedback for sensitive information, especially for private repositories. Git files and private operational credentials remain private. Deleting a record cannot recall copies already replicated by others.</p>
      <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={busy} onClick={verify}>{verifying ? "Verifying records…" : "Verify records"}</Button><Button variant="outline" disabled={busy} onClick={() => load()}>Review publication</Button><Button variant="outline" disabled={busy} onClick={reconcile}>Reconcile owner records</Button><Button variant="outline" disabled={busy} onClick={inspectConflict}>Inspect publication conflict</Button></div>
      {verification?.project_id === projectId && <PublicationVerificationResult result={verification} />}
      {conflict && <div className="space-y-3 rounded-md border p-3">
        <p>Conflict status: {conflict.status}</p>{conflict.status === "resumable" && <p className="text-sm">This mutation can still resume safely. Continue the original operation instead of discarding it.</p>}
        {conflict.status !== "none" && <><pre className="overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(conflict, null, 2)}</pre><p className="text-sm">Discarding abandons this pending local mutation and retains the current owner records. Inspect both digests before proceeding.</p><label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={discardConsent} disabled={busy} onChange={(event) => setDiscardConsent(event.target.checked)} />I reviewed this conflict and want to discard this pending mutation.</label><Button variant="destructive" disabled={busy || conflict.status !== "superseded" || !discardConsent || !conflict.intent_id || !conflict.current_manifest_cid} onClick={discardConflict}>Discard reviewed pending mutation</Button></>}
      </div>}
      {review && <>
        <p className="text-sm">{review.entry_count} records for <span className="break-all">{review.owner_did}</span>. Review digest: <code className="break-all">{review.digest}</code></p>
        {review.warnings.map((warning, i) => <p key={`${i}-${warning}`} className="text-sm">{warning}</p>)}
        <details><summary className="cursor-pointer">Review record values ({pages.length} pages loaded)</summary>{pages.map((page, i) => <pre key={i} className="my-2 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-md border p-3 text-xs">{page}</pre>)}</details>
        {review.next_cursor !== undefined ? <Button variant="outline" disabled={busy} onClick={() => load(true)}>Load next review page</Button> : <>
          <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={consent} disabled={busy} onChange={(event) => setConsent(event.target.checked)} />I reviewed this data and authorize its public publication to my ATProto account.</label>
          {review.has_telemetry && <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={telemetry} disabled={busy} onChange={(event) => setTelemetry(event.target.checked)} />I separately consent to publishing existing telemetry publicly. Prior telemetry opt-in does not grant this permission.</label>}
          <Button disabled={!consent || busy} onClick={publish}>{progress?.status === "pending" ? "Continue publication" : "Publish reviewed records"}</Button>
        </>}
      </>}
      {progress && <p role="status">Publication: {progress.status}{progress.total !== undefined ? ` (${progress.completed ?? 0}/${progress.total})` : ""}.</p>}
      {message && <p role="status" className="text-sm">{message}</p>}
      {busy && <Button variant="outline" onClick={() => { request.current?.abort(); setBusy(false); setVerifying(false); setMessage(verifying ? "Verification canceled. No records were published by this check." : "Paused locally. Published records remain durable; use the same review to continue."); }}>{verifying ? "Cancel verification" : "Pause requests"}</Button>}
    </CardContent>
  </Card>;
}
