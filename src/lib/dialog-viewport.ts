type Viewport = { height: number; offsetTop: number; scale: number }

// Ignore pinch zoom: it must remain under the user's control. Keyboard resizing
// at normal scale keeps the popup inside the visible portion of the page.
export function dialogViewport(layoutHeight: number, viewport?: Viewport | null) {
  if (!viewport || Math.abs(viewport.scale - 1) > .05 || !Number.isFinite(viewport.height) || viewport.height <= 0) return { height: layoutHeight, top: 0 }
  const height = Math.min(layoutHeight, viewport.height)
  const top = Math.max(0, Math.min(Number.isFinite(viewport.offsetTop) ? viewport.offsetTop : 0, layoutHeight - height))
  return { height, top }
}
