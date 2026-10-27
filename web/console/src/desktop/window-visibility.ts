/**
 * Window visibility, from the shell that now tracks it.
 *
 * The hand-written shell invented this: a context a window frame provides and the
 * content inside it reads, so a chart or a log tail stops polling while its window
 * is minimized — every request it makes is executed by some Cluster's Agent, and an
 * invisible window asking questions is load nobody can see.
 *
 * Kit ships the same mechanism, and its `WindowFrame` is what provides it now, so
 * this file is a re-export rather than an implementation. It stays because four
 * applications import it by this path and `desktop/window-visibility` is the name
 * the Console's own layering rules give it; deleting the file would mean editing
 * those applications, which a shell swap has no business doing.
 */
export {
  WindowVisibilityContext,
  pollWhileVisible,
  useWindowVisible,
} from "@kalandramo/desktop-kit";
