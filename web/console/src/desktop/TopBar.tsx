import { CircleDot, LayoutGrid, LogOut, Moon, Settings, Sun, Tag, UserRound } from "lucide-react";

import type { StreamState } from "@/api/events";
import { OpenSourceLink } from "@/components/common/open-source-link";
import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  HintTooltip,
  StatusDot,
} from "@kalandramo/desktop-kit/ui";
import { useSessionContext } from "@/auth/session-context";
import { formatRelative } from "@/lib/time";
import { ScopeSelector } from "@/scope/ScopeSelector";
import { useScopeStore } from "@/scope/scope-store";
import { useThemeStore } from "@/theme/theme-store";

const STREAM_LABELS: Record<
  StreamState,
  { label: string; tone: "success" | "warning" | "neutral" }
> = {
  open: { label: "实时同步", tone: "success" },
  connecting: { label: "连接中", tone: "warning" },
  reconnecting: { label: "重连中", tone: "warning" },
  closed: { label: "未连接", tone: "neutral" },
};

/**
 * The bar's lead: which Project the whole desktop is currently working in.
 *
 * It reads the scope store rather than taking props, because the store is what
 * every other consumer of the selection already reads — the bar showing the
 * Project is one more reader, not the owner of it.
 */
export function ScopeLead() {
  const scope = useScopeStore((state) => state.scope);
  const setScope = useScopeStore((state) => state.setScope);
  return <ScopeSelector scope={scope} onChange={setScope} />;
}

/** The theme toggle, in the slot kit reserves for it. */
export function ThemeControl() {
  const theme = useThemeStore((state) => state.theme);
  const toggleTheme = useThemeStore((state) => state.toggleTheme);
  return (
    <Button
      size="icon-sm"
      variant="ghost"
      onClick={toggleTheme}
      aria-label={theme === "dark" ? "切换到浅色主题" : "切换到深色主题"}
    >
      {theme === "dark" ? <Sun /> : <Moon />}
    </Button>
  );
}

/**
 * Who is signed in, and whether the cluster event stream is alive.
 *
 * The menu is the session badge: the operator's name is what it shows, and the
 * expiry, the build and the way out are what it holds. Splitting the trigger off
 * into a separate "badge" would put the same name on the bar twice.
 *
 * Order within the slot is the bar's own: event stream, then the source link, then
 * the menu — the way out belongs at the far right, where it was before the shell
 * swap. The kit top bar renders its right cluster as `{actions}{themeControl}{status}`
 * and that order is framework-fixed, so the theme control now sits to the left of
 * everything returned here; the menu is still the last thing on the bar.
 */
export function DesktopStatus({
  streamState,
  lastEventAt,
  onOpenApp,
  onResetDesktop,
}: {
  streamState: StreamState;
  lastEventAt: Date | null;
  onOpenApp: (appId: string) => void;
  onResetDesktop: () => void;
}) {
  const { session, logout, logoutPending } = useSessionContext();
  const stream = STREAM_LABELS[streamState];

  return (
    <>
      {/* Ambient status, so it is drawn as ambient: a dot and a quiet word. The
          pill it used to wear gave a thing that changes on its own the same
          weight as the controls beside it. */}
      <HintTooltip
        label={
          lastEventAt
            ? `集群状态事件流：${stream.label}，最近事件 ${formatRelative(lastEventAt)}`
            : `集群状态事件流：${stream.label}`
        }
      >
        {/* A step up from `subtle`: the bar no longer lifts the ground under it,
            so the faintest ink in it now stands on the wallpaper itself. */}
        <span className="text-muted-foreground mr-1 hidden items-center gap-1.5 text-[11px] sm:inline-flex">
          <StatusDot tone={stream.tone} />
          {stream.label}
        </span>
      </HintTooltip>

      <OpenSourceLink />

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="sm" className="gap-1.5 px-2 text-xs">
            <UserRound className="text-subtle-foreground" />
            <span className="max-w-32 truncate">{session?.user.display_name ?? "未登录"}</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuLabel>
            <span className="text-foreground block text-[13px] font-medium">
              {session?.user.display_name}
            </span>
            <span className="text-subtle-foreground block font-mono text-xs tabular-nums">
              {session?.user.username}
            </span>
          </DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => onOpenApp("settings")}>
            <Settings />
            系统设置与改密
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={onResetDesktop}>
            <LayoutGrid />
            重置桌面布局
          </DropdownMenuItem>
          <DropdownMenuItem disabled>
            <CircleDot />
            会话到期：{session ? formatRelative(session.expires_at) : "—"}
          </DropdownMenuItem>
          {/* Sits with 会话到期 rather than in the bar itself: the bar carries
              controls and one ambient status, and a build string pinned to it
              would be a permanent fixture nobody reads twice. Here it is one
              click from anywhere, which is what a version number is for.

              Stacked and wrapped rather than truncated with a tooltip on hover:
              a disabled menu item carries `pointer-events: none`, so neither a
              Tooltip nor a native `title` would ever fire on it, and an
              ellipsis with no way to see the rest is worse than two lines. An
              unstamped build reports Go's VCS pseudo-version, which is long
              enough that this is the normal case rather than the edge one. */}
          <DropdownMenuItem disabled className="items-start">
            <Tag className="mt-0.5" />
            <span className="grid min-w-0 gap-0.5">
              Server 版本
              <span className="text-subtle-foreground font-mono text-[11px] break-all tabular-nums">
                {session?.server_version || "—"}
              </span>
            </span>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="danger"
            disabled={logoutPending}
            onSelect={() => {
              void logout();
            }}
          >
            <LogOut />
            退出登录
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  );
}
