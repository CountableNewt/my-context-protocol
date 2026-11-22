"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangleIcon,
  CheckCircle2Icon,
  CircleDashedIcon,
  CopyIcon,
  RefreshCwIcon,
} from "lucide-react";
import {
  type CustomDomainStatus,
  fetchCustomDomain,
  setProjectCustomDomain,
  verifyProjectCustomDomain,
} from "@/lib/projects-api";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import { copyTextToClipboard } from "@/lib/clipboard";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

/** Matches MCP Catalog / Project Info top-level sections (page background, not bg-card). */
const SECTION_SHELL =
  "space-y-4 rounded-lg border p-4 text-sm text-card-foreground";

/** Matches MCP catalog monospace / URL inset panels. */
const INSET_SURFACE =
  "rounded-lg border border-border/80 bg-muted/35 dark:bg-muted/20";

type DnsRecord = NonNullable<CustomDomainStatus["platform_dns_records"]>[number];

function certificateStatusLabel(
  status: "not_configured" | "pending" | "issued" | "failed" | "unknown" | null | undefined,
) {
  switch (status) {
    case "issued":
      return "TLS issued";
    case "pending":
      return "TLS pending";
    case "failed":
      return "TLS failed";
    case "not_configured":
      return "TLS not configured";
    case "unknown":
      return "TLS unknown";
    default:
      return null;
  }
}

function certificateStatusTone(status: CustomDomainStatus["certificate_status"]) {
  if (status === "issued") {
    return "text-green-600 dark:text-green-500";
  }
  if (status === "failed" || status === "not_configured") {
    return "text-destructive";
  }
  return "text-muted-foreground";
}

function hasText(value: string | null | undefined) {
  return Boolean(value?.trim());
}

function dnsRecordGroups(data: CustomDomainStatus) {
  const hostname = data.hostname?.trim();
  const verification: DnsRecord[] = [];
  const token = data.ownership_verification_record_value || data.verification_token;

  if (hostname && hasText(token)) {
    verification.push({
      type: "TXT",
      name: data.ownership_verification_record_name?.trim() || data.verification_record_name?.trim() || `_mcp-verify.${hostname}`,
      value: token!.trim(),
    });
  }

  return { verification, platform: data.platform_dns_records ?? [] };
}

function dnsRecordStatusLabel(status: DnsRecord["status"]) {
  switch (status?.replace(/^DNS_RECORD_STATUS_/, "")) {
    case "PROPAGATED": return "Propagated";
    case "REQUIRES_UPDATE": return "Needs DNS update";
    case "PENDING": return "Pending propagation";
    default: return "Not confirmed";
  }
}

function visibleInstructions(data: CustomDomainStatus) {
  const instructions = data.instructions?.trim();
  if (!instructions) {
    return null;
  }

  const message = data.certificate_message?.trim();
  if (!message) {
    return instructions;
  }

  const lines = instructions
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line && line !== message);
  return lines.length > 0 ? lines.join("\n") : null;
}

function formatDnsRecordsForCopy(records: DnsRecord[]) {
  return records
    .map((record) => `${record.name}\t${record.type}\t${record.value}`)
    .join("\n");
}

function TlsStatusIcon({
  status,
  isPending,
}: {
  status: CustomDomainStatus["certificate_status"];
  isPending: boolean;
}) {
  if (isPending || status === "pending") {
    return <CircleDashedIcon className={cn("size-4", isPending && "animate-spin")} />;
  }
  if (status === "issued") {
    return <CheckCircle2Icon className="size-4" />;
  }
  if (status === "failed" || status === "not_configured" || status === "unknown") {
    return <AlertTriangleIcon className="size-4" />;
  }
  return <CircleDashedIcon className="size-4" />;
}

function DnsRecordList({
  title,
  records,
  action,
  showStatus = false,
}: {
  title: string;
  records: DnsRecord[];
  action?: {
    label: string;
    onClick: () => void;
  };
  showStatus?: boolean;
}) {
  return (
    <div
      className="overflow-hidden rounded-md border border-border/70"
    >
      <div className="flex items-center justify-between gap-2 border-b border-border/70 bg-muted/25 px-2.5 py-1.5">
        <span className="text-xs font-medium text-foreground">{title}</span>
        {action && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={action.onClick}
            className="h-7 px-2 text-xs"
          >
            <CopyIcon data-icon="inline-start" />
            {action.label}
          </Button>
        )}
      </div>
      <div className="divide-y divide-border/70">
        {records.map((record, index) => (
          <div
            key={`${record.type}-${record.name}-${record.value}-${index}`}
            className="grid gap-1 px-2.5 py-2 text-xs sm:grid-cols-[4.5rem_minmax(0,1fr)_minmax(0,1.4fr)] sm:gap-3"
          >
            <span className="font-medium text-foreground">{record.type}</span>
            <span className="break-all font-mono text-muted-foreground">{record.name}</span>
            <div className="flex flex-col gap-1">
              <span className="break-all font-mono text-foreground">{record.value}</span>
              {showStatus && <span className="text-muted-foreground">{dnsRecordStatusLabel(record.status)}</span>}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

interface CustomDomainSectionProps {
  projectId: string;
}

export function CustomDomainSection({ projectId }: CustomDomainSectionProps) {
  // Hooks must be called unconditionally before any early returns.
  const [hostname, setHostname] = useState("");
  const queryClient = useQueryClient();

  const { data, isLoading } = useQuery({
    queryKey: ["custom-domain", projectId],
    queryFn: () => fetchCustomDomain(projectId),
  });

  const setMutation = useMutation({
    mutationFn: () => setProjectCustomDomain(projectId, hostname.trim()),
    onMutate: () => queryClient.cancelQueries({ queryKey: ["custom-domain", projectId] }),
    onSuccess: async (next) => {
      // A read started before this write must not replace its newer result.
      await queryClient.cancelQueries({ queryKey: ["custom-domain", projectId] });
      queryClient.setQueryData(["custom-domain", projectId], next);
      queryClient.invalidateQueries({ queryKey: ["project", projectId] });
      setHostname("");
      verifyMutation.reset();
    },
  });

  const verifyMutation = useMutation({
    mutationFn: () => verifyProjectCustomDomain(projectId),
    onMutate: () => queryClient.cancelQueries({ queryKey: ["custom-domain", projectId] }),
    onSuccess: async (next) => {
      await queryClient.cancelQueries({ queryKey: ["custom-domain", projectId] });
      queryClient.setQueryData(["custom-domain", projectId], next);
      queryClient.invalidateQueries({ queryKey: ["project", projectId] });
    },
    onError: () => queryClient.invalidateQueries({ queryKey: ["custom-domain", projectId] }),
  });

  if (isLoading || !data) {
    return <Skeleton className="h-40 w-full" />;
  }

  const visibleData = data;
  const tlsLabel = certificateStatusLabel(visibleData.certificate_status);
  const canRefreshChecks = Boolean(visibleData.hostname);
  const verifyError =
    verifyMutation.error instanceof ApiError
      ? formatApiErrorDetail(verifyMutation.error.body) || verifyMutation.error.message
      : verifyMutation.error
        ? "Could not refresh DNS and TLS checks."
        : null;
  const isChecking = verifyMutation.isPending;
  const isMutating = isChecking || setMutation.isPending;
  const saveError = setMutation.error instanceof ApiError
    ? formatApiErrorDetail(setMutation.error.body) || setMutation.error.message
    : setMutation.error ? "Could not save the custom hostname." : null;
  const showCheckDetails =
    Boolean(visibleData.hostname) &&
    (isChecking ||
      visibleData.verified ||
      hasText(visibleData.certificate_message) ||
      Boolean(visibleData.platform_dns_records?.length));
  const recordGroups = dnsRecordGroups(visibleData);
  const hasDnsRecords = recordGroups.verification.length > 0 || recordGroups.platform.length > 0;
  const instructions = visibleInstructions(visibleData);

  function copyPlatformRecords() {
    void copyTextToClipboard(formatDnsRecordsForCopy(recordGroups.platform), {
      success: "Railway DNS records copied to clipboard",
      error: "Could not copy DNS records",
    });
  }

  return (
    <section
      className={SECTION_SHELL}
      aria-labelledby="custom-domain-heading"
    >
      <div className="space-y-1.5">
        <h2
          id="custom-domain-heading"
          className="text-base leading-snug font-medium text-foreground"
        >
          Custom Domain
        </h2>
        <p className="text-sm text-muted-foreground">
          Point your own hostname at this project. Add the project verification
          and Railway DNS records shown below, then verify. Routing becomes active after DNS and TLS verification.
        </p>
      </div>
      <div className="space-y-4">
        {visibleData.hostname && (
          <div>
            <span className="text-muted-foreground">Hostname: </span>
            <span className="font-medium">{visibleData.hostname}</span>
            {visibleData.verified ? (
              <span className="ml-2 text-green-600 dark:text-green-500">
                Verified
              </span>
            ) : (
              <span className="text-muted-foreground ml-2">
                Pending verification
              </span>
            )}
            {tlsLabel && (
              <span
                className={cn("ml-2", certificateStatusTone(visibleData.certificate_status))}
              >
                {tlsLabel}
              </span>
            )}
          </div>
        )}
        {hasDnsRecords && (
          <div className={cn(INSET_SURFACE, "space-y-3 p-3")}>
            <div className="border-b border-border/80 px-3 py-2 text-sm font-medium text-foreground">
              DNS records
            </div>
            {recordGroups.verification.length > 0 && (
              <DnsRecordList
                title="Project verification record"
                records={recordGroups.verification}
              />
            )}
            {recordGroups.platform.length > 0 && (
              <DnsRecordList
                title="Required Railway DNS records"
                records={recordGroups.platform}
                showStatus
                action={{ label: "Copy Railway records", onClick: copyPlatformRecords }}
              />
            )}
          </div>
        )}
        {showCheckDetails && (
          <div
            className={cn(INSET_SURFACE, "space-y-2 p-3")}
            aria-live="polite"
          >
            <div className="flex items-center gap-2 text-sm font-medium text-foreground">
              <TlsStatusIcon
                status={visibleData.certificate_status}
                isPending={isChecking}
              />
              {isChecking ? "Checking DNS and TLS" : "Latest DNS/TLS check"}
            </div>
            <ul className="space-y-1.5 text-xs text-muted-foreground">
              <li>
                Project domain verification:{" "}
                <span className={visibleData.verified ? "text-green-600 dark:text-green-500" : undefined}>
                  {visibleData.verified ? "verified" : isChecking ? "checking" : "waiting"}
                </span>
              </li>
              <li>
                Railway TLS certificate:{" "}
                <span className={certificateStatusTone(visibleData.certificate_status)}>
                  {isChecking
                    ? "requesting status"
                    : tlsLabel?.replace(/^TLS /, "").toLowerCase() ?? "not checked"}
                </span>
              </li>
            </ul>
            {hasText(visibleData.certificate_message) && (
              <p className="text-xs text-muted-foreground">
                {visibleData.certificate_message}
              </p>
            )}
          </div>
        )}
        {instructions && (
          <p
            className={cn(
              INSET_SURFACE,
              "whitespace-pre-wrap p-3 font-mono text-xs break-all",
            )}
          >
            {instructions}
          </p>
        )}
        {verifyError && (
          <p className="text-sm text-destructive">{verifyError}</p>
        )}
        {saveError && (
          <p className="text-sm text-destructive">{saveError}</p>
        )}
        <div className={cn(INSET_SURFACE, "space-y-2 p-3")}>
          <Label htmlFor="custom-host">Hostname</Label>
          <Input
            id="custom-host"
            value={hostname}
            onChange={(e) => setHostname(e.target.value)}
            disabled={isMutating}
            placeholder="mcp.example.com"
            className="bg-transparent dark:bg-transparent"
          />
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            size="sm"
            onClick={() => setMutation.mutate()}
            disabled={isMutating || !hostname.trim()}
          >
            {setMutation.isPending
              ? "Saving…"
              : data.hostname
                ? "Update Hostname"
                : "Save Hostname"}
          </Button>
          {canRefreshChecks && (
            <Button
              type="button"
              size="sm"
              variant="secondary"
              onClick={() => verifyMutation.mutate()}
              disabled={isMutating}
            >
              <RefreshCwIcon
                data-icon="inline-start"
                className={verifyMutation.isPending ? "animate-spin" : undefined}
              />
              {verifyMutation.isPending
                ? "Checking…"
                : visibleData.verified
                  ? "Refresh DNS/TLS"
                  : "Verify DNS"}
            </Button>
          )}
        </div>
      </div>
    </section>
  );
}
