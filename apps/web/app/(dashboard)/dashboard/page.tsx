import Link from "next/link";
import { AccountOverview } from "@/components/dashboard/account-overview";

export default function DashboardPage() {
  return (
    <div className="space-y-8">
      <div className="space-y-2">
        <h1 className="text-3xl font-bold tracking-tight">Dashboard</h1>
        <p className="w-full max-w-[66.6667vw] text-muted-foreground leading-relaxed">
          MCP traffic, latency, and catalog health across your projects.
        </p>
        <div className="flex flex-wrap gap-3 pt-1">
          <Link
            href="/projects"
            className="inline-flex h-9 items-center justify-center rounded-lg border border-transparent bg-primary px-4 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/80"
          >
            View Projects
          </Link>
        </div>
      </div>
      <AccountOverview />
    </div>
  );
}
