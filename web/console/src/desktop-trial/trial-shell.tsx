import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Badge, Button, TooltipProvider } from "@kalandramo/desktop-kit/ui";
import { Desktop, ShellRuntimeProvider } from "@kalandramo/desktop-kit";
import type { ShellRuntime } from "@kalandramo/desktop-kit";

import { customApplicationManifestId } from "@/apps/registry";
import { SessionContext, type SessionContextValue } from "@/auth/session-context";
import { ZkeMark } from "@/components/brand/zke-mark";
import { AppToaster } from "@/components/common/toaster";
import type { RegistrationCounts } from "@/desktop/bridge";
import { ScopeSelector } from "@/scope/ScopeSelector";
import { useScopeStore } from "@/scope/scope-store";

import { publishTrialCustomGroup } from "./dynamic-group";
import {
  nextTrialCustomAppGroup,
  TRIAL_CUSTOM_APP_GROUPS,
  type TrialCustomAppGroup,
  type TrialFixture,
} from "./fixture";

/**
 * 构建期由 vite.config.ts 注入的真实安装版本，不是 package.json 里的 pin。
 * 该包 exports 白名单不含 ./package.json，客户端 import 它会在构建期解析失败。
 */
declare const __DESKTOP_KIT_VERSION__: string;

const THEME_STORAGE_KEY = "zke.theme";
type ThemeName = "light" | "dark";

function readThemeName(): ThemeName {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

/** 与 index.html / kit-lab 同一套键：React 侧、快照侧、首帧脚本读的是同一份事实。 */
function applyTheme(next: ThemeName): void {
  document.documentElement.dataset.theme = next;
  try {
    localStorage.setItem(THEME_STORAGE_KEY, next);
  } catch {
    // 隐私模式写不进去不影响本次会话
  }
}

function ThemeSwitch() {
  const [theme, setTheme] = useState<ThemeName>(readThemeName);
  const apply = (next: ThemeName) => {
    applyTheme(next);
    setTheme(next);
  };
  return (
    <span className="flex items-center gap-1">
      <Button
        size="sm"
        variant={theme === "light" ? "secondary" : "ghost"}
        onClick={() => apply("light")}
      >
        浅色
      </Button>
      <Button
        size="sm"
        variant={theme === "dark" ? "secondary" : "ghost"}
        onClick={() => apply("dark")}
      >
        深色
      </Button>
    </span>
  );
}

/**
 * 动态通道与 restart 的两个开关。
 *
 * 放在顶栏 status 槽而不是浮层里：这一页要拍基线图，控件必须进图——一个只有点了才
 * 存在的入口，截图里看不出桌面挂着哪一组自定义应用，回归对比也就无从判断。
 *
 * 两个按钮各自只演示一件事，而且刻意不做成「智能」的：
 * - 切换项目走 `setScope` + `setDynamicApps` + 关孤儿窗口这条宿主顺序，一次换一组；
 * - 证据链跳转只调 `openApp(id, args, { restart: true })`，不碰清单，
 *   于是「标记变了」只能来自重挂载，不可能来自换组。
 */
function DynamicChannelControls({
  runtime,
  initialGroup,
}: {
  runtime: ShellRuntime;
  initialGroup: TrialCustomAppGroup | null;
}) {
  const setScope = useScopeStore((state) => state.setScope);
  const [group, setGroup] = useState<TrialCustomAppGroup | null>(initialGroup);
  const [closedOrphans, setClosedOrphans] = useState(0);

  const switchProject = () => {
    /*
     * 未装载时第一下装 A，而不是 B：这一页的 A 就是「当前项目的默认那一组」，
     * 从 off 起步的人要看到的是同一条链路的起点。
     */
    const next = group ? nextTrialCustomAppGroup(group.key) : TRIAL_CUSTOM_APP_GROUPS[0];
    /*
     * 先换作用域再换清单：应用是直接 `useScopeStore` 读项目的，顺序反过来会让新清单
     * 里刚出现的窗口先按旧项目渲染一帧。publishTrialCustomGroup 内部再保证
     * setDynamicApps 先于关孤儿窗口。
     */
    setScope(next.scope);
    setClosedOrphans(publishTrialCustomGroup(runtime, next));
    setGroup(next);
  };

  const jumpToEvidence = () => {
    const application = group?.applications[0];
    if (!application) {
      return;
    }
    runtime.openApp(
      customApplicationManifestId(application.project_id, application.id),
      { evidence: `trial:${application.id}` },
      { restart: true },
    );
  };

  return (
    <span className="flex items-center gap-1">
      <Button
        size="sm"
        variant="secondary"
        data-testid="trial-switch-project"
        onClick={switchProject}
      >
        切换项目
      </Button>
      <Button
        size="sm"
        variant="ghost"
        data-testid="trial-restart-evidence"
        disabled={!group}
        onClick={jumpToEvidence}
      >
        证据链跳转（restart）
      </Button>
      <span data-testid="trial-custom-group">{`组=${group?.label ?? "未装载"} 关孤儿=${closedOrphans}`}</span>
    </span>
  );
}

/**
 * 试跑页自己的 QueryClient。
 *
 * `retry: false` 与 `src/main.tsx` 的指数退避不同，而且必须不同：这里没有 Server，
 * 每个请求都会拿到 preview 的 SPA 回退页。留着重试只会把一次挂载报告变成三次同样的
 * 失败，把「这个应用渲染出了什么」埋在等待里。
 */
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false },
    mutations: { retry: false },
  },
});

/**
 * 夹具的会话上下文。
 *
 * `refresh` 与 `logout` 是空操作而不是省略：`SessionContextValue` 声明了这两个字段，
 * 而 系统设置 会渲染登出按钮。让它静默返回比让它真的清掉一个本就不存在的 Cookie 更
 * 诚实——页面上方那条夹具横幅已经说明了这里没有可退出的登录。
 */
function sessionValueFor(fixture: TrialFixture): SessionContextValue {
  return {
    status: "authenticated",
    session: fixture.session,
    permissions: fixture.permissions,
    refresh: () => {},
    logout: async () => {},
    logoutPending: false,
  };
}

export function TrialShell({
  fixture,
  counts,
  runtime,
}: {
  fixture: TrialFixture;
  counts: RegistrationCounts;
  runtime: ShellRuntime;
}) {
  /*
   * 作用域从 store 现读现给，和 desktop/TopBar.tsx 一模一样——这一页要验的就是
   * 「生产映射长什么样」，所以不另造一条数据通路。
   */
  const scope = useScopeStore((state) => state.scope);
  const setScope = useScopeStore((state) => state.setScope);
  return (
    <QueryClientProvider client={queryClient}>
      <SessionContext.Provider value={sessionValueFor(fixture)}>
        <TooltipProvider>
          <ShellRuntimeProvider runtime={runtime}>
            <Desktop
              /*
               * 尺寸必须由调用方给：ZkeMark 只负责填充与字形，「callers supply size」。
               * 少一个 className 就是让内层 svg 按替换元素的默认尺寸画成 300×300，
               * 顶栏是 absolute 且 z-30 浮在桌面上方，那块品牌面会直接盖住启动器第一行
               * 的瓦片——肉眼看着只是「品牌有点大」，机器上是第一扇窗口永远打不开。
               * 取值与 desktop/TopBar.tsx 逐字一致。
               */
              brand={<ZkeMark className="size-6 rounded-[8px]" />}
              lead={<ScopeSelector scope={scope} onChange={setScope} />}
              status={
                <span className="flex items-center gap-3">
                  <Badge tone="warning" data-testid="trial-fixture-badge">
                    夹具身份 · 无 Server
                  </Badge>
                  <DynamicChannelControls runtime={runtime} initialGroup={fixture.customGroup} />
                  <span data-testid="trial-session">{fixture.role}</span>
                  {/*
                    作用域读 store 而不是读 fixture：切换项目按钮换的就是 store，
                    这一行得跟着动，否则截图上看不出桌面到底挂在哪个项目上。
                    ?scope=none 时显示未选择，对照档自己也能被看见。
                  */}
                  <span data-testid="trial-scope">
                    {scope.projectName ? `项目=${scope.projectName}` : "未选择项目"}
                  </span>
                  <span data-testid="trial-counts">{`apps=${counts.apps} dock=${counts.dockItems} accents=${counts.accents}`}</span>
                  <span data-testid="trial-kit-version">{__DESKTOP_KIT_VERSION__}</span>
                </span>
              }
              themeControl={<ThemeSwitch />}
              /* 与 kit-lab 同一组 ZKE 目标行为，逐条对照见 src/kit-lab/main.tsx。 */
              revealPresentation="sliver"
              narrowViewportLayout="stacked"
              dockRunningAppClick="taskbar"
              windowShortcuts
            />
            <AppToaster />
          </ShellRuntimeProvider>
        </TooltipProvider>
      </SessionContext.Provider>
    </QueryClientProvider>
  );
}
