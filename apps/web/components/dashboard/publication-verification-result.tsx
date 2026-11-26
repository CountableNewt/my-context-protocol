import type { PublicationVerification } from "@/lib/atproto-projects-api";

const outcomes = {
  verified: ["All published records verified", "Every manifest reference was checked and the published records match the current project metadata."],
  not_published: ["Project records are not published", "No published project manifest was found. Review and publish the project’s records before verifying them."],
  missing: ["Published records are missing", "A published record could not be found. The check stopped at the first problem."],
  invalid: ["Published records do not match", "A record failed CID or content validation. Verification is not complete."],
  out_of_sync: ["Published metadata is out of sync", "The published records differ from the project’s current metadata."],
  unavailable: ["Verification unavailable", "The repository could not be checked. Retry when it is available; this does not mean records are missing."],
  incomplete: ["Verification incomplete", "The check did not finish. Run verification again to retry."],
} as const;

export function PublicationVerificationResult({ result }: { result: PublicationVerification }) {
  const [title, description] = outcomes[result.status] ?? outcomes.incomplete;
  return <section className="space-y-3 rounded-md border p-4" aria-label="Record verification result" role="status">
    <div className="space-y-1">
      <h3 className="font-medium">{title}</h3>
      <p className="text-muted-foreground text-sm">{description}</p>
    </div>
    <p className="text-sm">{result.checked_records} {result.checked_records === 1 ? "record" : "records"} checked in <span className="break-all font-mono">{result.owner_did}</span>.</p>
    {result.collections.length > 0 && <details>
      <summary className="cursor-pointer text-sm">Records checked by collection</summary>
      <dl className="mt-2 space-y-2 text-sm">{result.collections.map((entry) => <div className="flex items-start justify-between gap-4" key={entry.collection}>
        <dt className="min-w-0 break-all font-mono text-xs">{entry.collection}</dt><dd className="shrink-0 tabular-nums">{entry.checked_records}</dd>
      </div>)}</dl>
    </details>}
    {result.issues.length > 0 && <ul className="space-y-3 text-sm">{result.issues.map((issue, index) => <li key={`${issue.code}-${index}`} className="space-y-1">
      <p>{issue.message}</p>
      {issue.uri && <p className="break-all font-mono text-xs">{issue.uri}</p>}
      {(issue.expected_cid || issue.actual_cid) && <details><summary className="cursor-pointer text-xs">Record identity details</summary>
        {issue.expected_cid && <p className="break-all text-xs">Expected CID: {issue.expected_cid}</p>}
        {issue.actual_cid && <p className="break-all text-xs">Actual CID: {issue.actual_cid}</p>}
      </details>}
    </li>)}</ul>}
  </section>;
}
