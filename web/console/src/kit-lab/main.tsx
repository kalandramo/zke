import { StrictMode, lazy, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import { Boxes, Wrench } from "lucide-react";

import {
  createLocalStorageAdapter,
  createShellRuntime,
  Desktop,
  pollWhileVisible,
  ShellRuntimeProvider,
  useWindowVisible,
} from "@kalandramo/desktop-kit";
import type { AppManifest, DockItem } from "@kalandramo/desktop-kit/registry";
import { Badge, Button } from "@kalandramo/desktop-kit/ui";

import "@kalandramo/desktop-kit/styles.css";

/**
 * 容器查询探针。
 *
 * 两块内容宽度相同、类名相同，只有「是否自己声明 `@container`」不同：
 * kit 0.4.0 起窗口内容层自带 `@container`，所以**两块都应该排成两列**——
 * 这正是「应用里写 `@md:` 问的是窗口宽度而不是视口」的验证。
 * 在 kit ≤0.3.1 上，不带 `@container` 的那块会停在一列（容器查询找不到容器时不匹配）。
 */
function ContainerQueryProbe() {
  return (
    <div className="grid grid-cols-1 gap-4 p-4">
      <p className="text-xs text-muted-foreground">
        两块内容宽度相同、类名相同，只有「是否自己声明 @container」不同。
      </p>
      <div data-testid="cq-bare" className="grid @md:grid-cols-2 gap-2 rounded-panel border border-border p-2">
        <span className="rounded-control bg-surface-muted px-2 py-1 text-[11px]">bare A</span>
        <span className="rounded-control bg-surface-muted px-2 py-1 text-[11px]">bare B</span>
      </div>
      <div data-testid="cq-container" className="@container rounded-panel border border-border p-2">
        <div className="@md:grid-cols-2 grid gap-2">
          <span className="rounded-control bg-surface-muted px-2 py-1 text-[11px]">container A</span>
          <span className="rounded-control bg-surface-muted px-2 py-1 text-[11px]">container B</span>
        </div>
      </div>
    </div>
  );
}

function NotesApp() {
  // kit 的可见性契约：最小化后窗口仍然挂载，但这个 hook 会翻成 false，
  // 带 refetchInterval 的查询据此停摆（pollWhileVisible 就是这个判断的封装）。
  const visible = useWindowVisible();
  const interval = pollWhileVisible(visible, 5000);
  return (
    <div className="flex h-full min-h-0 flex-col gap-3 overflow-auto text-foreground">
      <div className="flex items-center gap-2 border-b border-border px-4 py-2.5">
        <span className="text-sm font-semibold">kit 验证应用</span>
        <Badge tone="primary">@kalandramo/desktop-kit</Badge>
        <span data-testid="visible-flag" className="text-[11px] text-muted-foreground">
          {visible ? "visible" : "hidden"} / interval={String(interval)}
        </span>
        <span className="ml-auto flex gap-2">
          <Button size="sm" variant="secondary">次操作</Button>
          <Button size="sm">主操作</Button>
        </span>
      </div>
      <ContainerQueryProbe />
    </div>
  );
}

function PlannedApp() {
  return <div className="p-4 text-sm text-muted-foreground">规划中的应用，不该被打开。</div>;
}

const runtime = createShellRuntime({
  adapter: createLocalStorageAdapter({ key: "zke.kit-lab.desktop" }),
});

const APPS: AppManifest[] = [
  {
    id: "lab-notes",
    title: "kit 验证",
    description: "挂载 desktop-kit 的桌面与组件",
    icon: Wrench,
    accent: "blue",
    availability: { state: "available" },
    defaultSize: { width: 720, height: 520 },
    singleton: true,
    mount: "react",
    entry: lazy(() => Promise.resolve({ default: NotesApp })),
  },
  {
    id: "lab-planned",
    title: "规划中",
    description: "计划态：无 accent，点击被外壳拒绝",
    icon: Boxes,
    availability: { state: "planned", phase: 1, plannedCapabilities: [] },
    defaultSize: { width: 480, height: 320 },
    mount: "react",
    entry: lazy(() => Promise.resolve({ default: PlannedApp })),
  },
];

const DOCK: DockItem[] = [
  { id: "dock-lab-notes", appId: "lab-notes", title: "kit 验证", icon: Wrench },
  { id: "dock-lab-planned", appId: "lab-planned", title: "规划中", icon: Boxes },
];

let registered = false;
function setup(): void {
  if (registered) return;
  registered = true;
  for (const app of APPS) runtime.registry.registerApp(app);
  for (const item of DOCK) runtime.registry.registerDockItem(item);
}

function ThemeSwitch() {
  const [theme, setTheme] = useState<"light" | "dark">(
    () => (document.documentElement.dataset.theme === "dark" ? "dark" : "light"),
  );
  const apply = (next: "light" | "dark") => {
    document.documentElement.dataset.theme = next;
    try {
      localStorage.setItem("zke.theme", next);
    } catch {
      // 隐私模式下写不进去不影响本次会话
    }
    setTheme(next);
  };
  return (
    <span className="flex items-center gap-1">
      <Button size="sm" variant={theme === "light" ? "secondary" : "ghost"} onClick={() => apply("light")}>
        浅色
      </Button>
      <Button size="sm" variant={theme === "dark" ? "secondary" : "ghost"} onClick={() => apply("dark")}>
        深色
      </Button>
    </span>
  );
}

function Lab() {
  setup();
  const layout = useMemo(
    () => ({ viewportWidth: window.innerWidth, viewportHeight: window.innerHeight }),
    [],
  );
  return (
    <ShellRuntimeProvider runtime={runtime}>
      <Desktop
        layout={layout}
        brand={<span className="text-sm font-semibold">desktop-kit lab</span>}
        status={<span className="text-xs text-muted-foreground">@kalandramo/desktop-kit@0.3.0</span>}
        themeControl={<ThemeSwitch />}
      />
    </ShellRuntimeProvider>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Lab />
  </StrictMode>,
);
