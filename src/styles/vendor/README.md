# shadcn 静态样式

`shadcn-tailwind.css` 原样来自 npm `shadcn@4.21.0` 的 `dist/tailwind.css`；许可证见同目录 `shadcn-LICENSE.md`。

本项目已有组件源码，只需要这份 Tailwind 样式。将样式作为源码保留，避免构建必须安装带有未修补 `braces` 拒绝服务漏洞的组件生成 CLI 及其依赖。没有引入 CLI JavaScript；新增组件请沿用仓库组件，待工具链漏洞修补后再评估生成工具。
