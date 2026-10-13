# Brand marks

OpenAI, Codex, Claude and Grok SVG marks are sourced from [Lobe Icons](https://github.com/lobehub/lobe-icons), package `@lobehub/icons-static-svg`, retrieved 2026-09-26. The original SVG paths are retained and rendered as CSS masks to support light/dark themes. The upstream MIT license is included in this directory. Brand names and marks belong to their respective owners.


## Duolingo Duo home animation

- Official page: https://www.duolingo.cn/ (retrieved 2026-10-03).
- Original Lottie: https://d35aaqx5ub95lt.cloudfront.net/lottie/splash/50bda50231c0bce1584e982cebfe8f33.json
- Local animation: duolingo-duo-home.json. Retain only the original Duo layers (comp_6) and their root parent; retain the original paths, masks, colors and keyframes. Use the official homepage loop, frames 130–290 at 30 fps. Crop the original composition to x=470, y=300, width=560, height=480 by translating its root parent.
- Local poster: duolingo-duo-home.svg, rendered from the same animation at frame 130 with the same crop. Used for small brand marks, loading/error fallback and reduced-motion preference.
- Runtime: lottie-web light SVG renderer, loaded separately; pause when offscreen or the document is hidden. No remote asset requests at runtime.
- Duolingo retains its trademark/artwork rights; the storefront identifies itself as an independent service provider.
