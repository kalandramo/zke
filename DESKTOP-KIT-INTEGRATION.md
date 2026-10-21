# desktop-kit 集成实验分支（branch-local，不向上游合并）

> 本文件只存在于 `kalandramo/zke` 的 `desktop-kit` 分支，用于记录该分支的目的与操作方式。
> **它不属于 ZKE 产品文档，不向上游 `togettoyou/zke` 提交。**

## 这个分支做什么

验证 `@kalandramo/desktop-kit`（从本仓库设计资产抽出的前端基础设施）能否在真实宿主里工作。分两步：

- **P1 令牌层**：把 Console 的 `src/styles/theme.css` 换成 `@kalandramo/desktop-kit-tokens/styles.css`；
- **P2 外壳层**：新增一个 dev-only 页面 `kit-lab.html`，直接挂载 kit 的 `<Desktop>`（含 Dock、窗口、主题切换），
  验证 peer 依赖、Tailwind `@source` 扫描、主题与容器查询在真实宿主里的行为。

## 改动清单

| 文件 | 改动 |
| --- | --- |
| `web/console/src/main.tsx` | `import "@/styles/theme.css"` → `import "@kalandramo/desktop-kit-tokens/styles.css"` |
| `web/console/package.json` / `pnpm-lock.yaml` | 新增依赖 `@kalandramo/desktop-kit-tokens ^0.3.0`、`@kalandramo/desktop-kit ^0.3.1` |
| `web/console/vite.config.ts` | 多入口：`index.html` + `kit-lab.html`（仅此分支） |
| `web/console/kit-lab.html` | lab 页外壳（复用 `zke.theme` 主题键） |
| `web/console/src/kit-lab/main.tsx` | lab 应用：注册两个假应用、挂 `<Desktop>`、主题切换、容器查询探针 |
| `web/console/pnpm-workspace.yaml` | 只用于对 `@kalandramo/*` 放行 pnpm 11 的 `minimumReleaseAge` 闸门（见下） |
| `src/styles/theme.css` | **保留未删**，让 P1 的 diff 可逆、可对照 |

## 已获得的结论

### P1：令牌层换装是零视觉变更

1440×960 固定视口截**登录页**（令牌用得最密的一屏），换装前后**逐像素完全一致**
（Playwright `toHaveScreenshot`，`maxDiffPixels: 0`；截图 SHA256 `403f200f…6a22f28`，599,103 B，两次相同）。
令牌包内的 `theme.css` 与上游 `20e9f39` 的 `web/console/src/styles/theme.css` 逐字节相同
（SHA256 `d9566ed7…4260`，61,449 B / 1,499 行）——换装没改任何设计取值，只改分发方式。

没有后端时先把启动期接口 mock 掉才能落到登录页；注意成功响应是**信封形状** `{ data: … }`
（`api/client.ts` 的 `unwrap` 取一层 `data`），少这一层会退回「无法读取系统初始化状态」。

### P2：外壳能在真实宿主里跑通，但**容器查询是硬阻塞**

`kit-lab.html` 在真实 Console（React 19 / Vite 8 / Tailwind v4.3）里验证通过：

- 令牌来自 npm 包：`--desktop-from` = `#f9fbfe`（ZKE `20e9f39` 的新取值）、`--planned-from` 可解析；
- kit 的组件类真的生成了 CSS：窗口 `border-radius` = `12px`（`rounded-window`），说明 `@source "./dist"` 在宿主
  的 Tailwind 管线里生效——这是「CSS 出口」设计在别人的工程里第一次被证明；
- Dock 两枚瓦片、点击开窗、计划态点击不出窗、主题切换（`--surface` `#fff` → `#272d37`）全部成立，页面无 JS 错误。

**容器查询实测**：lab 里放了两块内容宽度相同、类名相同（`@md:grid-cols-2`）的网格，只有「是否自己声明
`@container`」不同：

| 探针 | 计算后的列数 | 说明 |
| --- | --- | --- |
| 无 `@container` 祖先 | **1 列** | 容器查询找不到容器，`@md:` 永不成立——即使窗口有 720px 宽 |
| 自己声明 `@container` | **2 列** | 同一份类名立即生效 |

结论：**kit 的 `WindowFrame` 内容层必须加上 `@container`**，否则任何依赖容器查询的应用迁进来都会静默退化成一列。
这是迁移前的前置条件，与「缺 `useWindowVisible`（窗口不可见暂停轮询）」并列。

## P3：两条外壳契约已补进 kit（0.4.0），迁移阻塞解除

kit 0.4.0 补上了 P2 量出来的两条前置，本分支已升级并复验：

| 契约 | 0.3.1 的表现 | 0.4.0 的表现（本分支实测） |
| --- | --- | --- |
| 窗口内容层 `@container` | 应用里的 `@md:grid-cols-2` **恒为 1 列**（找不到容器） | 窗口内容宽 718px，**两块探针都是 2 列**——`@md:` 问的是窗口宽度，不再依赖视口 |
| `useWindowVisible()` / `pollWhileVisible()` | 不存在；最小化的窗口被直接卸载（状态丢失、轮询不停） | 最小化后窗口**仍挂载**（`hidden` + `data-visibility="minimized"`），应用内 `useWindowVisible()` 翻为 `false`，`pollWhileVisible(false, 5000)` 得到 `false`；还原后回到 `visible / interval=5000` |

lab 页的验证脚本现在断言：内容容器带 `@container`、两块 CQ 探针都是 2 列、最小化后 `hidden` + 可见性翻 false、
singleton 还原后回到 `on-screen`，且页面无 JS 错误。

注意语义变化：**窗口不再因最小化/显示桌面而卸载**。这是 ZKE 既有模型的采纳（终端、日志流、半填表单必须活下来），
代价是 DOM 常驻；应用的轮询要靠 `useWindowVisible()` 自己停，否则不可见窗口仍然在打集群 Agent。

## pnpm 策略例外（为什么有这个文件）

`web/console/pnpm-workspace.yaml` 只做一件事：对 `@kalandramo/*` 放行 pnpm 11 默认启用的
`minimumReleaseAge` 闸门（刚发布的包在锁文件校验期会被判为「过新」而拒绝安装）。它**不声明 `packages`**，
因此不会把 console 变成多包工作区；其它依赖仍然受闸门保护。

## 如何复现

1. `cd web/console && pnpm install && pnpm build`（产出 `dist/index.html` 与 `dist/kit-lab.html`）；
2. `pnpm exec vite preview --port 4319 --strictPort`；
3. 用 Playwright（`channel: msedge`，1440×960，`colorScheme: light`）：
   - 登录页：mock `/api/v1/setup`（`{data:{required:false}}`）与 `/api/v1/auth/me`（401），截图比对；
   - lab 页：直接打开 `/kit-lab.html`，断言 `--desktop-from`、窗口圆角、Dock 瓦片数、计划态不弹窗、
     两个容器查询探针的列数、主题切换后的 `--surface`。

## 同步上游

本分支长期存在并持续接收上游代码，**用 merge 不用 rebase**：

```bash
git fetch upstream main
git switch desktop-kit
git merge upstream/main
```

`upstream` = `https://github.com/togettoyou/zke.git`，**push URL 被显式禁用**（`DISABLED_no_push`）。
分支自身改动集中在 `src/kit-lab/`、`kit-lab.html`、`vite.config.ts` 与两个依赖，冲突概率低。
上游若继续演进 `theme.css`，需要先在 desktop-kit 里重跑令牌同步并发布新版本，再把这里的版本号改掉。

## 还没做

- 把 `@container` 与 `useWindowVisible` 做进 kit（前置条件）；
- 用 kit 的 11 个基础件替换 `components/ui`，再逐应用迁移（`window-store` 里带着 scope / 集群事件流等业务耦合）。

## 踩过的两个坑（供后来者）

1. **kit 0.2.1–0.3.0 的发布物里 `dependencies` 是 `workspace:*`**：那几步用了 `npm publish`，npm 不改写该协议、
   也不报错，消费方（本分支）`pnpm add` 时直接 `ERR_PNPM_WORKSPACE_PKG_NOT_FOUND`。kit 0.3.1 用 `pnpm publish`
   重发才修好；desktop-kit 仓库已加断言防止复发。
2. **把 `pnpm-workspace.yaml` 写进 `web/console` 会让它变成工作区根**，从而把依赖里的 `workspace:*` 当成工作区引用。
   所以本分支的例外文件必须在 kit 用真实版本发布之后才成立。