# desktop-kit 集成实验分支（branch-local，不向上游合并）

> 本文件只存在于 `kalandramo/zke` 的 `desktop-kit` 分支，用于记录该分支的目的与操作方式。
> **它不属于 ZKE 产品文档，不向上游 `togettoyou/zke` 提交。**

## 这个分支做什么

验证 `@kalandramo/desktop-kit`（从本仓库设计资产抽出的前端基础设施）能否在真实宿主里替换同类实现，
当前阶段只做**令牌层**：把 Console 的 `src/styles/theme.css` 换成 npm 包 `@kalandramo/desktop-kit-tokens/styles.css`。

改动只有两处：

- `web/console/src/main.tsx`：`import "@/styles/theme.css"` → `import "@kalandramo/desktop-kit-tokens/styles.css"`；
- `web/console/package.json`：新增依赖 `"@kalandramo/desktop-kit-tokens": "^0.3.0"`（随之更新 `pnpm-lock.yaml`）。

`src/styles/theme.css` **保留在原处未删除**：它现在是「未被引用但仍在仓库里」的文件，用来让这一步的 diff
保持可逆、可对照。

## 已验证的结论（2026-10-13）

在 1440×960 固定视口下截取**登录页**（本仓库令牌用得最密的一屏），换装前后**逐像素完全一致**
（Playwright `toHaveScreenshot`，`maxDiffPixels: 0`）：

| 版本 | SHA256（截图） | 字节 |
| --- | --- | --- |
| 用 `src/styles/theme.css` | `403f200f…6a22f28` | 599,103 B |
| 用 `@kalandramo/desktop-kit-tokens/styles.css` | `403f200f…6a22f28` | 599,103 B |

令牌包里的 `theme.css` 与上游 ZKE `20e9f39` 的 `web/console/src/styles/theme.css` 逐字节相同
（SHA256 `d9566ed7…4260`，61,449 B / 1,499 行）。也就是说：**换装没有改任何一个设计取值，只是改了分发方式。**

## 如何复现这次对比

Console 没有测试框架，对比用 desktop-kit 仓库里装好的 Playwright（`channel: msedge`）。没有后端时先把两个
启动期接口 mock 掉才能落到登录页；注意成功响应是信封形状 `{ data: … }`（`api/client.ts` 的 `unwrap` 取一层 `data`）：

```ts
// 关键 mock
await page.route("**/api/v1/setup**", (route) =>
  route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { required: false } }) }),
);
await page.route("**/api/v1/auth/me**", (route) =>
  route.fulfill({ status: 401, contentType: "application/json", body: JSON.stringify({ error: { code: "unauthenticated" } }) }),
);
```

复现步骤：

1. `cd web/console && pnpm install && pnpm build && pnpm exec vite preview --port 4319 --strictPort`；
2. 在 `desktop-kit` 仓库里用 Playwright 打开 `http://127.0.0.1:4319/`，视口 1440×960、`colorScheme: light`、
   `deviceScaleFactor: 1`，等 `document.fonts.ready` 后截图；
3. 换装前后各截一次，逐像素比对。

## 同步上游

本分支会长期存在并持续接收上游代码，**用 merge 不用 rebase**（rebase 会让共享分支的历史反复重写）：

```bash
git fetch upstream main
git switch desktop-kit
git merge upstream/main
```

`upstream` 已配置为 `https://github.com/togettoyou/zke.git`，且 **push URL 被显式禁用**（`DISABLED_no_push`），
避免误推到上游。分支自身的 diff 只有两行改动，冲突概率极低；一旦 `theme.css` 在上游继续演进，
令牌包需要重新同步（在 desktop-kit 仓库里更新 `packages/tokens/theme.css` 并发布新版本），本分支只改版本号。

## 后续（尚未做）

- **P2 外壳验证**：在 Console 里挂一个 dev-only 页面渲染 `@kalandramo/desktop-kit` 的 `<Desktop>`，
  验证 peer 依赖、Tailwind `@source` 扫描、主题切换在真实宿主里成立；
- 已知阻塞（迁移前必须先解决，否则应用会静默失效）：kit 的 `WindowFrame` 没有 `@container`，
  而 Console 的应用普遍用容器查询（`@md:` / `@2xl:`）按**窗口内容宽度**排版；kit 也还没有
  `useWindowVisible` 这类「窗口不可见时暂停轮询」的应用侧 API。
- 真正替换 `components/ui`（11 件）与 `desktop/`：需要先把上面两条做进 kit，再逐应用迁移。
