"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import {
  getCurrentUser,
  getGitHubLoginUrl,
  logout as apiLogout,
} from "@/lib/auth";
import { safeReturnPath } from "@/lib/safe-redirect";
import type { User } from "@/lib/types";

interface AuthContextValue {
  user: User | null;
  isLoading: boolean;
  loginWithGitHub: (returnTo?: string, selectAccount?: boolean) => void;
  logout: () => Promise<void>;
  refreshUser: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

// Module-level guard: prevents multiple confirmAuth calls when React Strict Mode remounts.
// The token is one-time use; a second call would get 401 and consume it before the first succeeds.
const confirmingTokens = new Set<string>();

declare global {
  interface Window {
    __confirmingAuthToken?: string;
  }
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const router = useRouter();
  const queryClient = useQueryClient();
  const authGeneration = useRef(0);
  const signingOut = useRef(false);

  const loadUser = useCallback(async () => {
    if (signingOut.current) return;
    const generation = ++authGeneration.current;
    try {
      const u = await getCurrentUser();
      if (generation !== authGeneration.current) return;
      setUser(u);
      // Recovery: redirect immediately when we have user + auth_failed, before any effect re-run
      if (u && typeof window !== "undefined" && new URLSearchParams(window.location.search).get("error") === "auth_failed") {
        window.location.replace("/dashboard");
        return;
      }
    } catch {
      if (generation === authGeneration.current) setUser(null);
    } finally {
      if (generation === authGeneration.current) setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    const params = typeof window !== "undefined" ? new URLSearchParams(window.location.search) : null;
    const authToken = params?.get("auth_token");

    // Add token synchronously at the very start so concurrent effect runs see it.
    // Use both module Set and window flag so it works across module instances (e.g. HMR).
    if (authToken) {
      if (confirmingTokens.has(authToken) || window.__confirmingAuthToken === authToken) {
        return;
      }
      confirmingTokens.add(authToken);
      window.__confirmingAuthToken = authToken;
    }

    if (authToken) {
      const returnUrl = new URL(window.location.href);
      returnUrl.searchParams.delete("auth_token");
      const relative = `${returnUrl.pathname}${returnUrl.search}${returnUrl.hash}`;
      const redirectTo = encodeURIComponent(relative);
      const confirmUrl = `/api/auth/confirm?token=${encodeURIComponent(authToken)}&redirect=${redirectTo}`;
      window.location.replace(confirmUrl);
      return;
    }

    if (!authToken) {
      queueMicrotask(() => {
        void loadUser();
      });
    }
  }, [loadUser]);

  // Recovery: when we have user but URL has auth_failed, do full-page nav so dashboard
  // loads fresh with session cookie (avoids React/Next.js layout transition losing state).
  useEffect(() => {
    if (user && typeof window !== "undefined") {
      const err = new URLSearchParams(window.location.search).get("error");
      if (err === "auth_failed") {
        window.location.replace("/dashboard");
      }
    }
  }, [user]);

  const loginWithGitHub = useCallback((returnTo = "/dashboard", selectAccount = false) => {
    returnTo = safeReturnPath(returnTo);
    window.location.href = getGitHubLoginUrl(returnTo, selectAccount);
  }, []);

  const logout = useCallback(async () => {
    if (signingOut.current) return;
    signingOut.current = true;
    ++authGeneration.current;
    try {
      await apiLogout();
      setUser(null);
      setIsLoading(false);
      queryClient.clear();
      router.replace("/login?select_account=1");
    } finally {
      signingOut.current = false;
    }
  }, [router, queryClient]);

  return (
    <AuthContext.Provider value={{ user, isLoading, loginWithGitHub, logout, refreshUser: loadUser }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error("useAuth must be used within AuthProvider");
  }
  return ctx;
}
