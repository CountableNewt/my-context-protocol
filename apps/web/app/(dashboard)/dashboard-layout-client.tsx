"use client";

import { AuthGuard } from "@/components/auth-guard";
import { EnvironmentBanner } from "@/components/dashboard/environment-banner";
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar";
import { AppSidebar } from "@/components/layout/app-sidebar";
import { Header } from "@/components/layout/header";

function DashboardLayoutInner({
  children,
  defaultSidebarOpen,
}: {
  children: React.ReactNode;
  defaultSidebarOpen: boolean;
}) {
  return (
    <>
      <SidebarProvider defaultOpen={defaultSidebarOpen}>
        <AppSidebar />
        <SidebarInset>
          <div className="sticky top-0 z-30 shrink-0">
            <EnvironmentBanner />
            <Header />
          </div>
          <div className="flex min-w-0 flex-1 flex-col gap-4 px-5 py-6 md:gap-5 md:px-6 md:py-8">
            {children}
          </div>
        </SidebarInset>
      </SidebarProvider>
    </>
  );
}

export function DashboardLayoutClient({
  children,
  defaultSidebarOpen,
}: {
  children: React.ReactNode;
  /** From `sidebar_state` cookie (server) so the first paint matches refresh/navigation. */
  defaultSidebarOpen: boolean;
}) {
  return (
    <AuthGuard>
      <DashboardLayoutInner defaultSidebarOpen={defaultSidebarOpen}>
        {children}
      </DashboardLayoutInner>
    </AuthGuard>
  );
}
