import { useState } from "react";

import type { AppComponentProps } from "@/apps/types";

/**
 * 每次内容层挂载自增一次的序号。
 *
 * 它是模块级的，因为要证明的正是「组件被重新挂载」这件事：把计数放在组件内部，
 * 重挂载会把计数一起清掉，标记就永远回到 1，测试看不出任何变化。放在模块上，
 * 同一扇窗口里序号必须跳一格，而窗口 id 不变——那才是 `restart` 的语义。
 */
let mountSequence = 0;

/**
 * 自定义应用的试跑内容。
 *
 * 生产上这一格是 `CustomApplicationFrame`——一个指向第三方源的 iframe。这里刻意
 * 换成一块本地面板：iframe 会真的去拉 `https://grafana.example.com`，而试跑页跑在
 * 没有 Server、没有外网的 preview 上，加载快慢与失败原因都不由外壳决定，用它做
 * 重挂载断言就是在测网络。面板里保留的三件事（应用名、目标地址、挂载标记）恰好是
 * 这条链路唯一需要被看见的东西。
 */
export function TrialCustomAppContent({ manifest }: AppComponentProps) {
  const [sequence] = useState(() => {
    mountSequence += 1;
    return mountSequence;
  });
  const application = manifest.customApplication;

  return (
    <div className="bg-surface flex h-full min-h-0 flex-col gap-3 p-4">
      <h3 className="text-foreground text-[15px] font-medium">{manifest.title}</h3>
      <p className="text-muted-foreground text-xs break-all">{application?.url}</p>
      <p className="text-subtle-foreground text-xs">
        这是试跑内容面板，不是生产的 iframe；它只用来证明重挂载。
      </p>
      <p
        data-testid="trial-custom-mount"
        className="border-border bg-surface-muted text-foreground rounded-panel border px-3 py-2 text-[13px]"
      >
        内容层挂载序号 <strong className="text-[15px] font-semibold">#{sequence}</strong>
      </p>
    </div>
  );
}

/*
 * 本文件只导出组件：`lazy(...)` 的包装放在 `dynamic-group.ts`，因为
 * `AppManifest["entry"]` 要求的是 `LazyExoticComponent`，而一个既导出组件又导出
 * 别的值的文件会让 fast refresh 整页失效。
 */
