"use client"

import * as React from "react"
import { mergeProps } from "@base-ui/react/merge-props"
import { useRender } from "@base-ui/react/use-render"
import { cva, type VariantProps } from "class-variance-authority"

import { useIsMobile } from "@/hooks/use-mobile"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { PanelLeftIcon } from "lucide-react"

import { MAIN_CONTENT_ID } from "@/lib/a11y"

const SIDEBAR_COOKIE_NAME = "sidebar_state"
const SIDEBAR_COOKIE_MAX_AGE = 60 * 60 * 24 * 7
/** Mirrors open state when cookies are blocked or lag; read on client init. */
const SIDEBAR_OPEN_STORAGE_KEY = "mycontext.sidebar.open"
const SIDEBAR_WIDTH_MOBILE = "18rem"
const SIDEBAR_WIDTH_ICON = "3rem"
const SIDEBAR_KEYBOARD_SHORTCUT = "b"
const SIDEBAR_WIDTH_STORAGE_KEY = "sidebar_width_px"
const DEFAULT_SIDEBAR_WIDTH_PX = 256
const MIN_SIDEBAR_WIDTH_PX = 200
const MAX_SIDEBAR_WIDTH_PX = 480

/** Matches project accordion panel timing (`app/globals.css` fan-in). */
const SIDEBAR_PANEL_DURATION_CLASS = "duration-[240ms]"
const SIDEBAR_PANEL_EASE_CLASS =
  "ease-[cubic-bezier(0.34,0.82,0.25,1)]"

/** Peek auto-close: slow pointer movement keeps this full hang time. */
const PEEK_AUTO_CLOSE_DELAY_MS_MAX = 200
/** Peek auto-close: fast movement off the panel approaches this floor. */
const PEEK_AUTO_CLOSE_DELAY_MS_MIN = 48
/** At or below this speed (px/ms), use {@link PEEK_AUTO_CLOSE_DELAY_MS_MAX}. */
const PEEK_POINTER_SPEED_SLOW_PX_PER_MS = 0.22
/** At or above this speed (px/ms), use {@link PEEK_AUTO_CLOSE_DELAY_MS_MIN}. */
const PEEK_POINTER_SPEED_FAST_PX_PER_MS = 1.35
/** Recent pointer segment used to estimate speed when leaving the peek rail. */
const PEEK_POINTER_HISTORY_MS = 72
const PEEK_POINTER_SAMPLES_MAX = 14

type PeekPointerSample = { x: number; y: number; t: number }

function computePeekAutoCloseDelayMs(samples: PeekPointerSample[]): number {
  if (samples.length < 2) return PEEK_AUTO_CLOSE_DELAY_MS_MAX
  const first = samples[0]
  const last = samples[samples.length - 1]
  const dt = last.t - first.t
  if (dt < 12) return PEEK_AUTO_CLOSE_DELAY_MS_MAX
  let dist = 0
  for (let i = 1; i < samples.length; i++) {
    dist += Math.hypot(
      samples[i].x - samples[i - 1].x,
      samples[i].y - samples[i - 1].y
    )
  }
  const v = dist / dt
  if (v <= PEEK_POINTER_SPEED_SLOW_PX_PER_MS) return PEEK_AUTO_CLOSE_DELAY_MS_MAX
  if (v >= PEEK_POINTER_SPEED_FAST_PX_PER_MS) return PEEK_AUTO_CLOSE_DELAY_MS_MIN
  const span = PEEK_POINTER_SPEED_FAST_PX_PER_MS - PEEK_POINTER_SPEED_SLOW_PX_PER_MS
  const t = (v - PEEK_POINTER_SPEED_SLOW_PX_PER_MS) / span
  return Math.round(
    PEEK_AUTO_CLOSE_DELAY_MS_MAX -
      t * (PEEK_AUTO_CLOSE_DELAY_MS_MAX - PEEK_AUTO_CLOSE_DELAY_MS_MIN)
  )
}

function clampSidebarWidth(px: number) {
  return Math.min(
    MAX_SIDEBAR_WIDTH_PX,
    Math.max(MIN_SIDEBAR_WIDTH_PX, Math.round(px))
  )
}

function readStoredSidebarWidth(): number | null {
  if (typeof window === "undefined") return null
  const stored = window.localStorage.getItem(SIDEBAR_WIDTH_STORAGE_KEY)
  if (!stored) return null
  const n = Number.parseInt(stored, 10)
  if (Number.isNaN(n)) return null
  return clampSidebarWidth(n)
}

function readStoredSidebarOpenFromLocalStorage(): boolean | null {
  if (typeof window === "undefined") return null
  try {
    const v = window.localStorage.getItem(SIDEBAR_OPEN_STORAGE_KEY)
    if (v === "0") return false
    if (v === "1") return true
  } catch {
    /* quota / private mode */
  }
  return null
}

function persistSidebarOpenPreference(openState: boolean) {
  if (typeof document === "undefined") return
  document.cookie = `${SIDEBAR_COOKIE_NAME}=${openState}; path=/; max-age=${SIDEBAR_COOKIE_MAX_AGE}; SameSite=Lax`
  try {
    window.localStorage.setItem(SIDEBAR_OPEN_STORAGE_KEY, openState ? "1" : "0")
  } catch {
    /* ignore */
  }
}

type SidebarContextProps = {
  state: "expanded" | "collapsed"
  open: boolean
  setOpen: (open: boolean) => void
  openMobile: boolean
  setOpenMobile: (open: boolean) => void
  isMobile: boolean
  toggleSidebar: () => void
  sidebarWidthPx: number
  setSidebarWidthPx: React.Dispatch<React.SetStateAction<number>>
  /** True while the panel was opened from the left-edge hover affordance (glass + auto-dismiss). */
  peekGlassActive: boolean
  handleEdgeHoverOpen: () => void
  handleSidebarPanelMouseEnter: () => void
  handleSidebarPanelMouseLeave: () => void
  /** Edge peek only: cancel auto-close while the pointer is over `SidebarTrigger`. */
  handlePeekTogglePointerEnter: () => void
  handlePeekTogglePointerLeave: () => void
}

const SidebarContext = React.createContext<SidebarContextProps | null>(null)

function useSidebar() {
  const context = React.useContext(SidebarContext)
  if (!context) {
    throw new Error("useSidebar must be used within a SidebarProvider.")
  }

  return context
}

function SidebarProvider({
  defaultOpen = true,
  open: openProp,
  onOpenChange: setOpenProp,
  className,
  style,
  children,
  ...props
}: React.ComponentProps<"div"> & {
  defaultOpen?: boolean
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  const isMobile = useIsMobile()
  const [openMobile, setOpenMobile] = React.useState(false)

  const [sidebarWidthPx, setSidebarWidthPxState] = React.useState(
    () => readStoredSidebarWidth() ?? DEFAULT_SIDEBAR_WIDTH_PX
  )
  const skipWidthPersistRef = React.useRef(true)

  const setSidebarWidthPx = React.useCallback(
    (value: React.SetStateAction<number>) => {
      setSidebarWidthPxState((prev) => {
        const next = typeof value === "function" ? value(prev) : value
        return clampSidebarWidth(next)
      })
    },
    []
  )

  React.useEffect(() => {
    if (skipWidthPersistRef.current) {
      skipWidthPersistRef.current = false
      return
    }
    if (typeof window === "undefined") return
    window.localStorage.setItem(
      SIDEBAR_WIDTH_STORAGE_KEY,
      String(sidebarWidthPx)
    )
  }, [sidebarWidthPx])

  const initialOpen = React.useMemo(
    () => readStoredSidebarOpenFromLocalStorage() ?? defaultOpen,
    [defaultOpen]
  )
  const [_open, _setOpen] = React.useState(initialOpen)
  const [peekGlassActive, setPeekGlassActive] = React.useState(false)
  const openedViaEdgeHoverRef = React.useRef(false)
  const edgeLeaveTimerRef = React.useRef<number | null>(null)
  const peekPointerSamplesRef = React.useRef<PeekPointerSample[]>([])

  const clearEdgeLeaveTimer = React.useCallback(() => {
    const id = edgeLeaveTimerRef.current
    if (id !== null) {
      window.clearTimeout(id)
      edgeLeaveTimerRef.current = null
    }
  }, [])

  React.useEffect(() => {
    return () => clearEdgeLeaveTimer()
  }, [clearEdgeLeaveTimer])

  const open = openProp ?? _open
  const setOpen = React.useCallback(
    (value: boolean | ((value: boolean) => boolean)) => {
      const openState = typeof value === "function" ? value(open) : value
      if (!openState) {
        openedViaEdgeHoverRef.current = false
        setPeekGlassActive(false)
        clearEdgeLeaveTimer()
      }
      if (setOpenProp) {
        setOpenProp(openState)
      } else {
        _setOpen(openState)
      }

      persistSidebarOpenPreference(openState)
    },
    [setOpenProp, open, clearEdgeLeaveTimer]
  )

  const handleEdgeHoverOpen = React.useCallback(() => {
    if (isMobile) return
    clearEdgeLeaveTimer()
    peekPointerSamplesRef.current = []
    openedViaEdgeHoverRef.current = true
    setPeekGlassActive(true)
    setOpen(true)
  }, [isMobile, clearEdgeLeaveTimer, setOpen])

  React.useEffect(() => {
    if (!peekGlassActive || isMobile) {
      peekPointerSamplesRef.current = []
      return
    }
    const onPointerMove = (ev: PointerEvent) => {
      const now = performance.now()
      const arr = peekPointerSamplesRef.current
      arr.push({ x: ev.clientX, y: ev.clientY, t: now })
      const cutoff = now - PEEK_POINTER_HISTORY_MS
      while (arr.length > 0 && arr[0].t < cutoff) arr.shift()
      if (arr.length > PEEK_POINTER_SAMPLES_MAX) {
        arr.splice(0, arr.length - PEEK_POINTER_SAMPLES_MAX)
      }
    }
    document.addEventListener("pointermove", onPointerMove, { passive: true })
    return () => document.removeEventListener("pointermove", onPointerMove)
  }, [peekGlassActive, isMobile])

  const handleSidebarPanelMouseEnter = React.useCallback(() => {
    clearEdgeLeaveTimer()
  }, [clearEdgeLeaveTimer])

  const schedulePeekAutoClose = React.useCallback(() => {
    if (isMobile || !openedViaEdgeHoverRef.current || !peekGlassActive) return
    clearEdgeLeaveTimer()
    const delayMs = computePeekAutoCloseDelayMs(peekPointerSamplesRef.current)
    edgeLeaveTimerRef.current = window.setTimeout(() => {
      edgeLeaveTimerRef.current = null
      openedViaEdgeHoverRef.current = false
      setPeekGlassActive(false)
      setOpen(false)
    }, delayMs)
  }, [isMobile, peekGlassActive, clearEdgeLeaveTimer, setOpen])

  const handleSidebarPanelMouseLeave = React.useCallback(() => {
    schedulePeekAutoClose()
  }, [schedulePeekAutoClose])

  const handlePeekTogglePointerEnter = React.useCallback(() => {
    if (!peekGlassActive) return
    clearEdgeLeaveTimer()
  }, [peekGlassActive, clearEdgeLeaveTimer])

  const handlePeekTogglePointerLeave = React.useCallback(() => {
    schedulePeekAutoClose()
  }, [schedulePeekAutoClose])

  // Helper to toggle the sidebar.
  const toggleSidebar = React.useCallback(() => {
    clearEdgeLeaveTimer()
    if (isMobile) {
      return setOpenMobile((o) => !o)
    }
    // Edge-hover peek already has open === true; flipping would collapse. First trigger
    // interaction commits to pinned open (layout gap + no auto-dismiss).
    if (peekGlassActive) {
      openedViaEdgeHoverRef.current = false
      setPeekGlassActive(false)
      return
    }
    openedViaEdgeHoverRef.current = false
    setPeekGlassActive(false)
    return setOpen((o) => !o)
  }, [
    isMobile,
    peekGlassActive,
    setOpen,
    setOpenMobile,
    clearEdgeLeaveTimer,
  ])

  // Adds a keyboard shortcut to toggle the sidebar.
  React.useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (
        event.key === SIDEBAR_KEYBOARD_SHORTCUT &&
        (event.metaKey || event.ctrlKey)
      ) {
        event.preventDefault()
        toggleSidebar()
      }
    }

    window.addEventListener("keydown", handleKeyDown)
    return () => window.removeEventListener("keydown", handleKeyDown)
  }, [toggleSidebar])

  // We add a state so that we can do data-state="expanded" or "collapsed".
  // This makes it easier to style the sidebar with Tailwind classes.
  const state = open ? "expanded" : "collapsed"

  const contextValue = React.useMemo<SidebarContextProps>(
    () => ({
      state,
      open,
      setOpen,
      isMobile,
      openMobile,
      setOpenMobile,
      toggleSidebar,
      sidebarWidthPx,
      setSidebarWidthPx,
      peekGlassActive,
      handleEdgeHoverOpen,
      handleSidebarPanelMouseEnter,
      handleSidebarPanelMouseLeave,
      handlePeekTogglePointerEnter,
      handlePeekTogglePointerLeave,
    }),
    [
      state,
      open,
      setOpen,
      isMobile,
      openMobile,
      setOpenMobile,
      toggleSidebar,
      sidebarWidthPx,
      setSidebarWidthPx,
      peekGlassActive,
      handleEdgeHoverOpen,
      handleSidebarPanelMouseEnter,
      handleSidebarPanelMouseLeave,
      handlePeekTogglePointerEnter,
      handlePeekTogglePointerLeave,
    ]
  )

  return (
    <SidebarContext.Provider value={contextValue}>
      <div
        data-slot="sidebar-wrapper"
        style={
          {
            "--sidebar-width": `${sidebarWidthPx}px`,
            "--sidebar-width-icon": SIDEBAR_WIDTH_ICON,
            ...style,
          } as React.CSSProperties
        }
        className={cn(
          "group/sidebar-wrapper flex min-h-svh w-full min-w-0 has-data-[variant=inset]:bg-sidebar",
          className
        )}
        {...props}
      >
        {children}
        <SidebarEdgeHoverZone />
      </div>
    </SidebarContext.Provider>
  )
}

/** Narrow hit target at the left viewport edge: opens the desktop off-canvas sidebar (glass peek). */
function SidebarEdgeHoverZone() {
  const { isMobile, open, handleEdgeHoverOpen } = useSidebar()
  if (isMobile || open) return null
  return (
    <div
      data-slot="sidebar-edge-hover"
      className="pointer-events-auto fixed inset-y-0 left-0 z-[35] hidden w-3 bg-transparent md:block"
      aria-hidden
      onMouseEnter={handleEdgeHoverOpen}
    />
  )
}

function Sidebar({
  side = "left",
  variant = "sidebar",
  collapsible = "offcanvas",
  className,
  children,
  dir,
  ...props
}: React.ComponentProps<"div"> & {
  side?: "left" | "right"
  variant?: "sidebar" | "floating" | "inset"
  collapsible?: "offcanvas" | "icon" | "none"
}) {
  const {
    isMobile,
    state,
    openMobile,
    setOpenMobile,
    peekGlassActive,
    handleSidebarPanelMouseEnter,
    handleSidebarPanelMouseLeave,
  } = useSidebar()

  if (collapsible === "none") {
    return (
      <div
        data-slot="sidebar"
        className={cn(
          "flex h-full w-(--sidebar-width) flex-col bg-sidebar text-sidebar-foreground",
          className
        )}
        {...props}
      >
        {children}
      </div>
    )
  }

  if (isMobile) {
    return (
      <Sheet open={openMobile} onOpenChange={setOpenMobile} {...props}>
        <SheetContent
          dir={dir}
          data-sidebar="sidebar"
          data-slot="sidebar"
          data-mobile="true"
          className={cn(
            "w-(--sidebar-width) bg-sidebar p-0 text-sidebar-foreground [&>button]:hidden",
            SIDEBAR_PANEL_DURATION_CLASS,
            SIDEBAR_PANEL_EASE_CLASS,
            // Sheet defaults fade the whole panel; keep opacity up so row cascades read clearly.
            "data-starting-style:opacity-100 data-ending-style:opacity-100",
            side === "left" &&
              "data-[side=left]:data-starting-style:translate-x-[-0.3rem] data-[side=left]:data-ending-style:translate-x-[-0.3rem]",
            side === "right" &&
              "data-[side=right]:data-starting-style:translate-x-[0.3rem] data-[side=right]:data-ending-style:translate-x-[0.3rem]"
          )}
          style={
            {
              "--sidebar-width": SIDEBAR_WIDTH_MOBILE,
            } as React.CSSProperties
          }
          side={side}
        >
          <SheetHeader className="sr-only">
            <SheetTitle>Sidebar</SheetTitle>
            <SheetDescription>Displays the mobile sidebar.</SheetDescription>
          </SheetHeader>
          <div className="flex h-full w-full flex-col">{children}</div>
        </SheetContent>
      </Sheet>
    )
  }

  return (
    <div
      className="group peer hidden text-sidebar-foreground md:block"
      data-state={state}
      data-collapsible={state === "collapsed" ? collapsible : ""}
      data-variant={variant}
      data-side={side}
      data-slot="sidebar"
    >
      {/* This is what handles the sidebar gap on desktop */}
      <div
        data-slot="sidebar-gap"
        className={cn(
          "relative w-(--sidebar-width) bg-transparent transition-[width]",
          SIDEBAR_PANEL_DURATION_CLASS,
          SIDEBAR_PANEL_EASE_CLASS,
          "motion-reduce:transition-none",
          "group-data-[collapsible=offcanvas]:w-0",
          // Edge-hover peek: keep layout width at 0 so the panel floats over content.
          peekGlassActive &&
            collapsible === "offcanvas" &&
            "!w-0 min-w-0 shrink-0 overflow-hidden",
          "group-data-[side=right]:rotate-180",
          variant === "floating" || variant === "inset"
            ? "group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)+(--spacing(4)))]"
            : "group-data-[collapsible=icon]:w-(--sidebar-width-icon)"
        )}
      />
      <div
        data-slot="sidebar-container"
        data-side={side}
        className={cn(
          "fixed inset-y-0 z-10 hidden h-svh w-(--sidebar-width) transition-[left,right,width]",
          SIDEBAR_PANEL_DURATION_CLASS,
          SIDEBAR_PANEL_EASE_CLASS,
          "motion-reduce:transition-none",
          // Pinned expanded: above sticky env banner (z-30) so layer order matches glass peek (no z flip on toggle).
          state === "expanded" &&
            collapsible === "offcanvas" &&
            !isMobile &&
            !peekGlassActive &&
            "z-40",
          // Edge peek: above in-flow chrome (header ~h-14) and banner.
          peekGlassActive && collapsible === "offcanvas" && "z-[60] shadow-xl",
          "data-[side=left]:left-0 data-[side=left]:group-data-[collapsible=offcanvas]:left-[calc(var(--sidebar-width)*-1)] data-[side=right]:right-0 data-[side=right]:group-data-[collapsible=offcanvas]:right-[calc(var(--sidebar-width)*-1)] md:flex",
          // Adjust the padding for floating and inset variants.
          variant === "floating" || variant === "inset"
            ? "p-2 group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)+(--spacing(4))+2px)]"
            : "group-data-[collapsible=icon]:w-(--sidebar-width-icon) group-data-[side=left]:border-r group-data-[side=right]:border-l",
          className
        )}
        {...props}
      >
        <div
          data-sidebar="sidebar"
          data-slot="sidebar-inner"
          onMouseEnter={
            !isMobile && collapsible === "offcanvas"
              ? handleSidebarPanelMouseEnter
              : undefined
          }
          onMouseLeave={
            !isMobile && collapsible === "offcanvas"
              ? handleSidebarPanelMouseLeave
              : undefined
          }
          className={cn(
            "relative flex size-full flex-col bg-sidebar group-data-[variant=floating]:rounded-lg group-data-[variant=floating]:shadow-sm group-data-[variant=floating]:ring-1 group-data-[variant=floating]:ring-sidebar-border",
            // While the inset gap animates (peek → pinned), ease the panel to full opacity in lockstep.
            collapsible === "offcanvas" &&
              state === "expanded" &&
              cn(
                "transition-opacity",
                SIDEBAR_PANEL_DURATION_CLASS,
                SIDEBAR_PANEL_EASE_CLASS,
                "motion-reduce:transition-none",
              ),
            peekGlassActive &&
              cn(
                "supports-backdrop-filter:backdrop-blur-[6px]",
                "border-0 border-transparent",
                // Soft top edge where the rail meets the env strip (diffused highlight, not a hard rule).
                "shadow-[inset_0_1px_0_0_rgba(255,255,255,0.07)]",
                "bg-sidebar/50 supports-backdrop-filter:bg-sidebar/22",
                "opacity-100",
              ),
            // Offcanvas hides via `left` on the container only — do not fade this wrapper,
            // or nav row cascades run while opacity is still ~0 and read as “no animation”.
            collapsible === "offcanvas" &&
              state === "collapsed" &&
              "pointer-events-none"
          )}
        >
          {children}
        </div>
      </div>
    </div>
  )
}

function SidebarResizeHandle({
  className,
  side = "left",
  ...props
}: React.ComponentProps<"button"> & {
  side?: "left" | "right"
}) {
  const { sidebarWidthPx, setSidebarWidthPx, state, isMobile, peekGlassActive } =
    useSidebar()

  React.useEffect(() => {
    return () => {
      document.documentElement.classList.remove("sidebar-resize-dragging")
    }
  }, [])

  /* Edge-peek overlay: handle sits on the right rail and overlaps the shifted header toggle. */
  if (isMobile || state === "collapsed" || peekGlassActive) return null

  return (
    <button
      type="button"
      tabIndex={-1}
      aria-hidden
      data-slot="sidebar-resize-handle"
      className={cn(
        "absolute inset-y-0 z-50 cursor-col-resize touch-none border-0 bg-transparent p-0 select-none",
        "w-3 hover:bg-sidebar-border/50",
        "focus-visible:bg-sidebar-border/50 focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:outline-none",
        side === "left"
          ? "-right-1.5 translate-x-1/2"
          : "-left-1.5 -translate-x-1/2",
        className
      )}
      onPointerDown={(e) => {
        e.preventDefault()
        if (e.button !== 0) return
        const startX = e.clientX
        const startW = sidebarWidthPx
        const el = e.currentTarget
        el.setPointerCapture(e.pointerId)
        document.documentElement.classList.add("sidebar-resize-dragging")

        const onMove = (ev: PointerEvent) => {
          const dx = ev.clientX - startX
          const next = side === "left" ? startW + dx : startW - dx
          setSidebarWidthPx(next)
        }

        const end = (ev: PointerEvent) => {
          document.documentElement.classList.remove("sidebar-resize-dragging")
          try {
            el.releasePointerCapture(ev.pointerId)
          } catch {
            /* already released */
          }
          el.removeEventListener("pointermove", onMove)
          el.removeEventListener("pointerup", end)
          el.removeEventListener("pointercancel", end)
        }

        el.addEventListener("pointermove", onMove)
        el.addEventListener("pointerup", end)
        el.addEventListener("pointercancel", end)
      }}
      {...props}
    />
  )
}

function SidebarTrigger({
  className,
  onClick,
  ...props
}: React.ComponentProps<typeof Button>) {
  const { toggleSidebar } = useSidebar()

  return (
    <Button
      data-sidebar="trigger"
      data-slot="sidebar-trigger"
      variant="ghost"
      size="icon-sm"
      className={cn(className)}
      onClick={(event) => {
        onClick?.(event)
        toggleSidebar()
      }}
      {...props}
    >
      <PanelLeftIcon aria-hidden />
      <span className="sr-only">Toggle Sidebar</span>
    </Button>
  )
}

function SidebarInset({
  className,
  tabIndex,
  "aria-label": ariaLabel,
  ...props
}: Omit<React.ComponentProps<"main">, "id">) {
  return (
    <main
      data-slot="sidebar-inset"
      className={cn(
        "relative flex min-w-0 w-full flex-1 flex-col bg-background md:peer-data-[variant=inset]:m-2 md:peer-data-[variant=inset]:ml-0 md:peer-data-[variant=inset]:rounded-xl md:peer-data-[variant=inset]:shadow-sm md:peer-data-[variant=inset]:peer-data-[state=collapsed]:ml-2",
        className
      )}
      {...props}
      id={MAIN_CONTENT_ID}
      tabIndex={tabIndex ?? -1}
      aria-label={ariaLabel ?? "Main content"}
    />
  )
}

function SidebarHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-header"
      data-sidebar="header"
      className={cn("flex flex-col gap-2 px-2.5 py-2", className)}
      {...props}
    />
  )
}

function SidebarFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-footer"
      data-sidebar="footer"
      className={cn("flex flex-col gap-2 px-2.5 py-2", className)}
      {...props}
    />
  )
}

function SidebarContent({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-content"
      data-sidebar="content"
      className={cn(
        "no-scrollbar flex min-h-0 flex-1 flex-col gap-0 overflow-auto group-data-[collapsible=icon]:overflow-hidden",
        className
      )}
      {...props}
    />
  )
}

function SidebarGroup({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-group"
      data-sidebar="group"
      className={cn("relative flex w-full min-w-0 flex-col px-2.5 py-2", className)}
      {...props}
    />
  )
}

function SidebarGroupContent({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-group-content"
      data-sidebar="group-content"
      className={cn("w-full text-sm", className)}
      {...props}
    />
  )
}

function SidebarMenu({ className, ...props }: React.ComponentProps<"ul">) {
  return (
    <ul
      data-slot="sidebar-menu"
      data-sidebar="menu"
      className={cn("flex w-full min-w-0 flex-col gap-2", className)}
      {...props}
    />
  )
}

function SidebarMenuItem({ className, ...props }: React.ComponentProps<"li">) {
  return (
    <li
      data-slot="sidebar-menu-item"
      data-sidebar="menu-item"
      className={cn("group/menu-item relative", className)}
      {...props}
    />
  )
}

const sidebarMenuButtonVariants = cva(
  "peer/menu-button group/menu-button flex w-full items-center gap-2 overflow-hidden rounded-md px-2.5 py-2 text-left text-sm ring-sidebar-ring outline-hidden transition-[width,height,padding] group-has-data-[sidebar=menu-action]/menu-item:pr-8 group-data-[collapsible=icon]:size-8! group-data-[collapsible=icon]:p-2! hover:bg-sidebar-accent/58 hover:text-sidebar-accent-foreground focus-visible:ring-2 active:bg-sidebar-accent/58 active:text-sidebar-accent-foreground disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 data-open:hover:bg-sidebar-accent/58 data-open:hover:text-sidebar-accent-foreground data-active:bg-sidebar-accent data-active:font-medium data-active:text-sidebar-accent-foreground data-active:hover:bg-sidebar-accent [&_svg]:size-4 [&_svg]:shrink-0 [&>span:last-child]:truncate",
  {
    variants: {
      variant: {
        default: "hover:bg-sidebar-accent/58 hover:text-sidebar-accent-foreground",
        outline:
          "bg-background shadow-[0_0_0_1px_hsl(var(--sidebar-border))] hover:bg-sidebar-accent/58 hover:text-sidebar-accent-foreground hover:shadow-[0_0_0_1px_hsl(var(--sidebar-accent))]",
      },
      size: {
        default: "h-8 text-sm",
        sm: "h-7 text-xs",
        lg: "h-12 text-sm group-data-[collapsible=icon]:p-0!",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function SidebarMenuButton({
  render,
  isActive = false,
  variant = "default",
  size = "default",
  tooltip,
  className,
  ...props
}: useRender.ComponentProps<"button"> &
  React.ComponentProps<"button"> & {
    isActive?: boolean
    tooltip?: string | React.ComponentProps<typeof TooltipContent>
  } & VariantProps<typeof sidebarMenuButtonVariants>) {
  const { isMobile, state } = useSidebar()
  const comp = useRender({
    defaultTagName: "button",
    props: mergeProps<"button">(
      {
        className: cn(sidebarMenuButtonVariants({ variant, size }), className),
      },
      props
    ),
    render: !tooltip ? render : <TooltipTrigger render={render} />,
    state: {
      slot: "sidebar-menu-button",
      sidebar: "menu-button",
      size,
      active: isActive,
    },
  })

  if (!tooltip) {
    return comp
  }

  if (typeof tooltip === "string") {
    tooltip = {
      children: tooltip,
    }
  }

  return (
    <Tooltip>
      {comp}
      <TooltipContent
        side="right"
        align="center"
        hidden={state !== "collapsed" || isMobile}
        {...tooltip}
      />
    </Tooltip>
  )
}

function SidebarMenuAction({
  className,
  render,
  showOnHover = false,
  ...props
}: useRender.ComponentProps<"button"> &
  React.ComponentProps<"button"> & {
    showOnHover?: boolean
  }) {
  return useRender({
    defaultTagName: "button",
    props: mergeProps<"button">(
      {
        className: cn(
          "absolute top-1.5 right-1 flex aspect-square w-5 items-center justify-center rounded-md p-0 text-sidebar-foreground ring-sidebar-ring outline-hidden transition-transform group-data-[collapsible=icon]:hidden peer-hover/menu-button:text-sidebar-accent-foreground peer-data-[size=default]/menu-button:top-1.5 peer-data-[size=lg]/menu-button:top-2.5 peer-data-[size=sm]/menu-button:top-1 after:absolute after:-inset-2 hover:bg-sidebar-accent/58 hover:text-sidebar-accent-foreground focus-visible:ring-2 md:after:hidden [&>svg]:size-4 [&>svg]:shrink-0",
          showOnHover &&
            "group-focus-within/menu-item:opacity-100 group-hover/menu-item:opacity-100 peer-data-active/menu-button:text-sidebar-accent-foreground aria-expanded:opacity-100 md:opacity-0",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-menu-action",
      sidebar: "menu-action",
    },
  })
}

function SidebarMenuSub({ className, ...props }: React.ComponentProps<"ul">) {
  return (
    <ul
      data-slot="sidebar-menu-sub"
      data-sidebar="menu-sub"
      className={cn(
        "mx-3.5 flex min-w-0 translate-x-px flex-col gap-0.5 border-l border-sidebar-border px-2 py-0 group-data-[collapsible=icon]:hidden",
        className
      )}
      {...props}
    />
  )
}

function SidebarMenuSubItem({
  className,
  ...props
}: React.ComponentProps<"li">) {
  return (
    <li
      data-slot="sidebar-menu-sub-item"
      data-sidebar="menu-sub-item"
      className={cn("group/menu-sub-item relative", className)}
      {...props}
    />
  )
}

function SidebarMenuSubButton({
  render,
  size = "md",
  isActive = false,
  className,
  ...props
}: useRender.ComponentProps<"a"> &
  React.ComponentProps<"a"> & {
    size?: "sm" | "md"
    isActive?: boolean
  }) {
  return useRender({
    defaultTagName: "a",
    props: mergeProps<"a">(
      {
        className: cn(
          "flex h-6.5 min-w-0 -translate-x-px items-center gap-1.5 overflow-hidden rounded-md px-2 text-sidebar-foreground ring-sidebar-ring outline-hidden group-data-[collapsible=icon]:hidden hover:bg-sidebar-accent/58 hover:text-sidebar-accent-foreground focus-visible:ring-2 active:bg-sidebar-accent/58 active:text-sidebar-accent-foreground disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 data-[size=md]:text-sm data-[size=sm]:text-xs data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground data-active:hover:bg-sidebar-accent [&>span:last-child]:truncate [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-sidebar-accent-foreground",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-menu-sub-button",
      sidebar: "menu-sub-button",
      size,
      active: isActive,
    },
  })
}

export {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarResizeHandle,
  SidebarTrigger,
  sidebarMenuButtonVariants,
  useSidebar,
}
