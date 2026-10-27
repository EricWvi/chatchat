/*
 * Whether the last input came from a pointer. Menus and dialogs give focus
 * back to their trigger when they close; after a pointer interaction that
 * would leave a focus ring (and open the trigger's tooltip), so they skip
 * it then. Keyboard users still get focus back.
 */
let pointer = false;

window.addEventListener("pointerdown", () => (pointer = true), true);
window.addEventListener("keydown", () => (pointer = false), true);

/** For onCloseAutoFocus: keeps focus where it is after a pointer interaction. */
export function skipFocusAfterPointer(e: Event) {
  if (pointer) e.preventDefault();
}
