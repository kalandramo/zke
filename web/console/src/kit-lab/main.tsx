import { StrictMode, lazy, useState } from "react";
import { createRoot } from "react-dom/client";
import { Boxes, FlaskConical, Monitor, Wrench } from "lucide-react";

import {
  createLocalStorageAdapter,
  createShellRuntime,
  Desktop,
  pollWhileVisible,
  ShellRuntimeProvider,
  useWindowVisible,
} from "@kalandramo/desktop-kit";
import type { AccentDefinition, AppManifest, DockItem } from "@kalandramo/desktop-kit/registry";
import { Badge, Button } from "@kalandramo/desktop-kit/ui";
import { appAccentVarNames } from "@kalandramo/desktop-kit-tokens";

import "@kalandramo/desktop-kit/styles.css";

/**
 * 真实版本号，由 vite.config.ts 在构建期用 fs 读 node_modules 里的 package.json 注入。
 * 不能在这里 import 那个 package.json——构建期按包的 exports 解析，而它不在白名单里，
 * 所以只能在 Node 侧读完了塞进来。硬编码会过期：这里曾经写着 0.3.0，而 pin 早已是 0.5.0。
 */
declare const __DESKTOP_KIT_VERSION__: string;

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
      <p className="text-muted-foreground text-xs">
        两块内容宽度相同、类名相同，只有「是否自己声明 @container」不同。
      </p>
      <div
        data-testid="cq-bare"
        className="rounded-panel border-border grid gap-2 border p-2 @md:grid-cols-2"
      >
        <span className="rounded-control bg-surface-muted px-2 py-1 text-[11px]">bare A</span>
        <span className="rounded-control bg-surface-muted px-2 py-1 text-[11px]">bare B</span>
      </div>
      <div data-testid="cq-container" className="rounded-panel border-border @container border p-2">
        <div className="grid gap-2 @md:grid-cols-2">
          <span className="rounded-control bg-surface-muted px-2 py-1 text-[11px]">
            container A
          </span>
          <span className="rounded-control bg-surface-muted px-2 py-1 text-[11px]">
            container B
          </span>
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
    <div className="text-foreground flex h-full min-h-0 flex-col gap-3 overflow-auto">
      <div className="border-border flex items-center gap-2 border-b px-4 py-2.5">
        <span className="text-sm font-semibold">kit 验证应用</span>
        <Badge tone="primary">@kalandramo/desktop-kit</Badge>
        <span data-testid="visible-flag" className="text-muted-foreground text-[11px]">
          {visible ? "visible" : "hidden"} / interval={String(interval)}
        </span>
        <span className="ml-auto flex gap-2">
          <Button size="sm" variant="secondary">
            次操作
          </Button>
          <Button size="sm">主操作</Button>
        </span>
      </div>
      <ContainerQueryProbe />
    </div>
  );
}

function PlannedApp() {
  return <div className="text-muted-foreground p-4 text-sm">规划中的应用，不该被打开。</div>;
}

/**
 * 读三件套的**计算值**。三件套在模块作用域的 setup() 里就写到 <html> 上了，
 * 所以挂载时读一次就是终值，不需要 effect 再订阅一次。
 */
function readAccentVars(): Record<string, string> {
  const style = getComputedStyle(document.documentElement);
  const vars = appAccentVarNames(LAB_ACCENT.id);
  return {
    [vars.from]: style.getPropertyValue(vars.from).trim(),
    [vars.to]: style.getPropertyValue(vars.to).trim(),
    [vars.glow]: style.getPropertyValue(vars.glow).trim(),
  };
}

/**
 * 宿主自定义 accent 的验证应用。读计算值而不是注册值：registry 里存着定义不代表瓦片
 * 有颜色——少补一步，`var()` 解析不到值，整条 background-image 在计算值阶段失效，
 * 瓦片静默渲染成无色，而 tsc / eslint / vite build 全都报绿，只有计算值能揭穿它。
 */
function AccentApp() {
  const [resolved] = useState(readAccentVars);
  const vars = appAccentVarNames(LAB_ACCENT.id);
  return (
    <div className="text-foreground flex h-full min-h-0 flex-col gap-3 overflow-auto p-4">
      <span className="text-sm font-semibold">宿主补帧：自定义 accent「{LAB_ACCENT.id}」</span>
      <p className="text-muted-foreground text-xs">
        registry 只存定义；把三件套写到 &lt;html&gt;
        是宿主的活。下面读的是计算值，任何一行空着就说明那一档没补上。
      </p>
      <dl
        data-testid="accent-vars"
        className="text-muted-foreground flex flex-col gap-1 text-[11px]"
      >
        {Object.entries(resolved).map(([name, value]) => (
          <div key={name} className="flex gap-2">
            <dt className="font-mono">{name}</dt>
            <dd className="text-foreground font-mono">{value === "" ? "（未定义）" : value}</dd>
          </div>
        ))}
      </dl>
      <div
        data-testid="accent-swatch"
        className="rounded-panel h-16 w-16"
        style={{
          backgroundImage:
            "linear-gradient(to bottom, var(" + vars.from + "), var(" + vars.to + "))",
        }}
      />
    </div>
  );
}

const THEME_STORAGE_KEY = "zke.theme";
type ThemeName = "light" | "dark";

function readThemeName(): ThemeName {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

/** 主题的唯一写入口：DOM 属性 + 与 index.html 同一套键，React 侧和快照侧读的是同一份事实。 */
function applyTheme(next: ThemeName): void {
  document.documentElement.dataset.theme = next;
  try {
    localStorage.setItem(THEME_STORAGE_KEY, next);
  } catch {
    // 隐私模式下写不进去不影响本次会话
  }
}

/**
 * 以下取值是 ZKE 目标行为，kit 的缺省值不是这个，逐条对照：
 * - `snapMode: "edge"`（缺省 `classic` 八区）。4px 阈值不用单独配：core 的
 *   `EDGE_SNAP_THRESHOLD` 恒为 4，而 kit 的拖拽钩子在 edge 分支不传 `edgeThreshold`，
 *   所以 `edge` 本身就意味着 4px（classic 那侧才是 24px）。
 * - `cascade: true`（缺省 false，两扇同尺寸窗口会精确重叠、第一扇被完全遮住）。
 * - `getTheme`/`setTheme`：不传 `getTheme` 时 persist 永远写 light/system，
 *   `restore()` 回来的主题就是假的——这条是持久化闭环能不能算验证过的分水岭。
 */
const runtime = createShellRuntime({
  adapter: createLocalStorageAdapter({ key: "zke.kit-lab.desktop" }),
  snapMode: "edge",
  cascade: true,
  getTheme: () => ({ name: readThemeName(), preference: readThemeName() }),
  setTheme: (theme) => {
    applyTheme(theme.preference === "dark" ? "dark" : "light");
  },
});

/** 自定义档：注册处是 lab 里唯一允许出现字面色值的地方，组件一律只引用变量。 */
const LAB_ACCENT: AccentDefinition = {
  id: "lab-coral",
  from: "#ff8f6b",
  to: "#f0564a",
  glow: "rgb(240 86 74 / 0.42)",
};

/** 把三件套落到 `<html>` 的内联变量——kit 只存定义、不碰 DOM，这一步必须由宿主做。 */
function applyAccentVars(accent: AccentDefinition): void {
  const vars = appAccentVarNames(accent.id);
  const root = document.documentElement;
  root.style.setProperty(vars.from, accent.from);
  root.style.setProperty(vars.to, accent.to);
  root.style.setProperty(vars.glow, accent.glow);
}

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
  {
    id: "lab-accent",
    title: "自定义色",
    description: "registerAccent + 宿主补帧的三件套",
    icon: FlaskConical,
    accent: LAB_ACCENT.id,
    availability: { state: "available" },
    defaultSize: { width: 520, height: 380 },
    singleton: true,
    mount: "react",
    entry: lazy(() => Promise.resolve({ default: AccentApp })),
  },
];

const DOCK: DockItem[] = [
  { id: "dock-lab-notes", appId: "lab-notes", title: "kit 验证", icon: Wrench },
  { id: "dock-lab-planned", appId: "lab-planned", title: "规划中", icon: Boxes },
  { id: "dock-lab-accent", appId: "lab-accent", title: "自定义色", icon: FlaskConical },
  /*
   * 「显示桌面」是 revealPresentation 的唯一入口：没有它，"sliver" 与缺省的 "hidden"
   * 在页面上看不出任何区别，这条 ZKE 目标行为就只是类型通过而已。
   */
  {
    id: "dock-show-desktop",
    title: "显示桌面",
    icon: Monitor,
    accent: "slate",
    onSelect: () => {
      runtime.toggleDesktopRevealed();
    },
  },
];

let registered = false;

/**
 * 注册必须先于 `<Desktop>` 挂载（挂载即 freeze()，之后所有 register* 只 warn 并忽略），
 * 也必须先于 `restore()`：恢复守卫 knownAppIds / singletonAppIds 是从 registry 现算的，
 * 空注册表会让两个集合被整体省略，快照就按原样恢复而不做未知应用过滤。
 */
function setup(): void {
  if (registered) return;
  registered = true;
  runtime.registry.registerAccent(LAB_ACCENT);
  applyAccentVars(LAB_ACCENT);
  for (const app of APPS) runtime.registry.registerApp(app);
  for (const item of DOCK) runtime.registry.registerDockItem(item);
}

/** restore() 的回报留在模块作用域，好让顶栏把「持久化闭环真的跑过」显示出来。 */
let restoreReport = "未运行";

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

function Lab() {
  return (
    <ShellRuntimeProvider runtime={runtime}>
      <Desktop
        brand={<span className="text-sm font-semibold">desktop-kit lab</span>}
        status={
          <span className="text-muted-foreground flex items-center gap-2 text-xs">
            <span data-testid="kit-version">@kalandramo/desktop-kit@{__DESKTOP_KIT_VERSION__}</span>
            <span data-testid="restore-report">{restoreReport}</span>
          </span>
        }
        themeControl={<ThemeSwitch />}
        /*
         * 以下四项同样是 ZKE 目标行为，kit 缺省依次是 hidden / multi-window /
         * launch / false。留在这里是为了 P5c 换装时能逐条对照，而不是换完才发现
         * 外壳行为变了。不传 `layout`：kit 的 `useViewportSize` 会活跟随真实视口，
         * 而 `layout` 是覆盖契约——一旦传一个 useMemo 快照就把视口钉死在首帧那个数，
         * 窗口几何就不再随窗口大小变了。
         */
        revealPresentation="sliver"
        narrowViewportLayout="stacked"
        dockRunningAppClick="taskbar"
        windowShortcuts
      />
    </ShellRuntimeProvider>
  );
}

setup();

// 恢复先于首帧：窗口集合与几何在第一次渲染时就位，避免闪一帧空桌面再弹窗。
// fail-soft 由 runtime 内部兜底（读失败 / 坏快照 → 冷启动），lab 不再另加兜底。
const restored = await runtime.restore();
restoreReport = "restore=" + String(restored.restored) + (restored.dropped ? " dropped" : "");

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Lab />
  </StrictMode>,
);

// autosave 在恢复之后开启：先订阅会把 restore() 自己那次 store 写入也当成变更落盘。
runtime.startAutosave();
