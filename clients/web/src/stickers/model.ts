// The user's sticker palette: sticker ids in hex, in the user's order. The
// backend's order wins; these show a change before its event arrives.

/** The most stickers a palette holds. */
export const MAX_PALETTE = 200;

/** The palette with the sticker added at the end, unless already there or full. */
export function withSaved(palette: string[], id: string): string[] {
  if (palette.includes(id) || palette.length >= MAX_PALETTE) return palette;
  return [...palette, id];
}

export function withoutSticker(palette: string[], id: string): string[] {
  return palette.includes(id) ? palette.filter((s) => s !== id) : palette;
}

/** The palette with the sticker moved before another, or to the end when before is empty. */
export function moved(palette: string[], id: string, before: string): string[] {
  if (!palette.includes(id) || id === before || (before && !palette.includes(before))) return palette;
  const rest = palette.filter((s) => s !== id);
  const at = before ? rest.indexOf(before) : rest.length;
  const next = [...rest.slice(0, at), id, ...rest.slice(at)];
  return next.every((s, i) => s === palette[i]) ? palette : next;
}

/**
 * What to move the sticker before to move it one place up (-1) or down (+1):
 * a sticker id, or empty for the end; null where it cannot move that way.
 */
export function beforeFor(palette: string[], id: string, step: -1 | 1): string | null {
  const i = palette.indexOf(id);
  if (i < 0) return null;
  if (step < 0) return i > 0 ? palette[i - 1] : null;
  if (i >= palette.length - 1) return null;
  return palette[i + 2] ?? "";
}
