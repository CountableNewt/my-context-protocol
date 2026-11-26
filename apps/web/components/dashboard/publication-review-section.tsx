"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { fetchPublicationReview, publishProjectReview, verifyProjectRecords, type PublicationVerification, reconcileProjectRecords, fetchPublicationConflict, discardPublicationConflict, type PublicationConflict, type PublicationProgress, type PublicationReview } from "@/lib/atproto-projects-api";
import { PublicationVerificationResult } from "./publication-verification-result";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export function PublicationReviewSection({ projectId, reviewRequest = 0 }: { projectId: string; reviewRequest?: number }) {
  const [conflict, setConflict] = useState<PublicationConflict | null>(null);
  const [discardConsent, setDiscardConsent] = useState(false);
  const [review, setReview] = useState<PublicationReview | null>(null);
  const [publication, setPublication] = useState<{ review: PublicationReview; telemetry: boolean } | null>(null);
  const publishButton = useRef<HTMLButtonElement | null>(null);
  const [includeTelemetry, setIncludeTelemetry] = useState(false);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<PublicationProgress | null>(null);
  const [verification, setVerification] = useState<PublicationVerification | null>(null);
  const [verifying, setVerifying] = useState(false);
  const [message, setMessage] = useState("");
  const request = useRef<AbortController | null>(null);
  const client = useQueryClient();
  useEffect(() => () => request.current?.abort(), [projectId]);
  const begin = useCallback(() => { request.current?.abort(); const next = new AbortController(); request.current = next; setBusy(true); setMessage(""); setVerification(null); return next; }, []);
  const report = useCallback((error: unknown, signal: AbortSignal) => { if (!signal.aborted) setMessage(error instanceof ApiError ? formatApiErrorDetail(error.body) || error.message : "The operation could not finish. You can retry safely."); }, []);
  async function verifyWithAction(action: AbortController) {
    setVerifying(true);
    try {
      const result = await verifyProjectRecords(projectId, action.signal);
      if (action.signal.aborted) return;
      if (result.project_id !== projectId) throw new Error("Verification returned a different project");
      setVerification(result);
      return result;
    } finally { if (request.current === action) setVerifying(false); }
  }
  async function verify() {
    const action = begin();
    try { await verifyWithAction(action); }
    catch (error) { report(error, action.signal); }
    finally { if (!action.signal.aborted) setBusy(false); }
  }
  useEffect(() => {
    if (!reviewRequest) return;
    document.getElementById("atproto-records")?.scrollIntoView?.({ behavior: "smooth", block: "start" });
    publishButton.current?.focus({ preventScroll: true });
  }, [reviewRequest]);
  async function publish() {
    const action = begin();
    let active = publication;
    try {
      if (!active) {
        const next = await fetchPublicationReview(projectId, 0, action.signal, includeTelemetry);
        if (action.signal.aborted) return;
        if (next.project_id !== projectId || (!includeTelemetry && next.has_telemetry)) throw new Error("Publication returned an unexpected scope");
        active = { review: next, telemetry: includeTelemetry };
        setReview(next); setPublication(active); setProgress(null);
      }
      // The click authorizes this publication scope. Keep its digest across
      // bounded requests and retries so partial writes remain resumable.
      while (!action.signal.aborted) {
        const next = await publishProjectReview(projectId, active.review.digest, active.telemetry, action.signal);
        if (action.signal.aborted) return;
        setProgress(next);
        if (next.digest !== active.review.digest) {
          setPublication(null); setProgress(null);
          setMessage("Project data changed. Select Publish records to try again with the current data.");
          return;
        }
        if (next.status === "complete") {
          setPublication(null);
          void client.invalidateQueries({ queryKey: ["project", projectId] });
          void client.invalidateQueries({ queryKey: ["projects"] });
          void client.invalidateQueries({ queryKey: ["repo-connection", projectId] });
          const result = await verifyWithAction(action);
          if (result?.status === "verified") setMessage("ATProto migration complete. Project records published and verified.");
          else if (!action.signal.aborted) setMessage("Publication finished, but verification has not passed. Check the result and retry verification.");
          return;
        }
        if (next.status !== "pending") {
          setMessage("Publication has not completed. Retry to continue safely.");
          return;
        }
      }
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) { setPublication(null); setReview(null); setProgress(null); }
      report(error, action.signal);
    } finally { if (request.current === action) setVerifying(false); if (!action.signal.aborted) setBusy(false); }
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
      if (!action.signal.aborted) { setMessage(`Conflict resolution: ${next.status}. Reload the publication review before writing again.`); setConflict(null); setDiscardConsent(false); setPublication(null); setReview(null); setProgress(null); }
    } catch (error) { setDiscardConsent(false); report(error, action.signal); }
    finally { if (!action.signal.aborted) setBusy(false); }
  }
  async function reconcile() {
    const action = begin();
    try { const next = await reconcileProjectRecords(projectId, action.signal); if (!action.signal.aborted) { setMessage(`Reconciliation: ${next.status}.`); void client.invalidateQueries(); } }
    catch (error) { report(error, action.signal); }
    finally { if (!action.signal.aborted) setBusy(false); }
  }
  return <Card id="atproto-records"><CardHeader><CardTitle>ATProto records</CardTitle><CardDescription>Publish project metadata to your ATProto account.</CardDescription></CardHeader>
    <CardContent className="space-y-4">
      <p className="text-sm">Publishing creates public MCP records in your account’s PDS. Git files and credentials stay private. Telemetry is excluded by default.</p>
      <div className="flex flex-wrap gap-2">
        <Button ref={publishButton} disabled={busy} onClick={publish}>{busy && !verifying ? "Publishing records…" : publication ? "Continue publication" : "Publish records"}</Button>
        <Button variant="outline" disabled={busy} onClick={verify}>{verifying ? "Verifying records…" : "Verify records"}</Button>
      </div>
      {progress && <p role="status">Publication: {progress.status}{progress.total !== undefined ? ` (${progress.completed ?? 0}/${progress.total})` : ""}.</p>}
      {verification?.project_id === projectId && <PublicationVerificationResult result={verification} />}
      {message && <p role="status" className="text-sm">{message}</p>}
      {busy && <Button variant="outline" onClick={() => { request.current?.abort(); setBusy(false); setVerifying(false); setMessage(verifying ? (progress?.status === "complete" ? "Verification canceled. Publication remains complete; verify records again to finish confirmation." : "Verification canceled. No records were published by this check.") : "Publication paused. Select the publish action to resume safely."); }}>{verifying ? "Cancel verification" : "Pause publication"}</Button>}
      <details className="space-y-3">
        <summary className="cursor-pointer text-sm">Advanced</summary>
        <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={includeTelemetry} disabled={busy || publication !== null} onChange={(event) => { setIncludeTelemetry(event.target.checked); setReview(null); setProgress(null); setVerification(null); }} />Publish existing telemetry publicly with this project</label>
        <p className="text-muted-foreground text-sm">Without telemetry publication, runtime telemetry is disabled and existing history stays private. Public records may be replicated by others.</p>
        <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={busy} onClick={reconcile}>Reconcile owner records</Button><Button variant="outline" disabled={busy} onClick={inspectConflict}>Inspect publication conflict</Button></div>
        {conflict && <div className="space-y-3 rounded-md border p-3">
          <p>Conflict status: {conflict.status}</p>{conflict.status === "resumable" && <p className="text-sm">This mutation can still resume safely. Continue the original operation instead of discarding it.</p>}
          {conflict.status !== "none" && <><pre className="overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(conflict, null, 2)}</pre><p className="text-sm">Discarding abandons this pending local mutation and retains the current owner records. Inspect both digests before proceeding.</p><label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={discardConsent} disabled={busy} onChange={(event) => setDiscardConsent(event.target.checked)} />I reviewed this conflict and want to discard this pending mutation.</label><Button variant="destructive" disabled={busy || conflict.status !== "superseded" || !discardConsent || !conflict.intent_id || !conflict.current_manifest_cid} onClick={discardConflict}>Discard reviewed pending mutation</Button></>}
        </div>}
        {review && <details><summary className="cursor-pointer text-sm">Publication details</summary>
          <p className="text-sm">{review.entry_count} records for <span className="break-all">{review.owner_did}</span>. Digest: <code className="break-all">{review.digest}</code></p>
          {review.warnings.map((warning, i) => <p key={`${i}-${warning}`} className="text-sm">{warning}</p>)}
          <pre className="my-2 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-md border p-3 text-xs">{review.entries_json}</pre>
        </details>}
      </details>
    </CardContent>
  </Card>;
}
