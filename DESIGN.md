# Builda Web UI

Builda is an operations console for starting configured jobs and inspecting their queue, agents, history, and logs. The interface puts status and the next useful action first. It uses a quiet surface hierarchy, one blue action color, compact dividers, and readable working space. It has no marketing hero, decorative imagery, or ornamental motion.

## Tokens

The source of truth is `web/src/styles/base.css`. The root palette defines light mode; `data-color-scheme="dark"` selects the dark palette. The saved preference (`light`, `dark`, or `system`) remains in `data-theme`, and the startup script resolves system preference before the first paint.

| Role | Light | Dark |
| --- | --- | --- |
| App background | `#f6f8fa` | `#010409` |
| Surface | `#ffffff` | `#0d1117` |
| Inset surface | `#f0f3f6` | `#161b22` |
| Main text | `#1f2328` | `#e6edf3` |
| Secondary text | `#59636e` | `#8b949e` |
| Border | `#d0d7de` | `#30363d` |
| Control boundary | `#838e9b` | `#697584` |
| Action | `#0969da` | `#58a6ff` |
| Focus ring | `#0969da` | `#58a6ff` |

Success, attention, and danger tokens describe run and agent states. Status text always names the state; color only reinforces it. Selected rows use a tinted accent surface with an accent border, preserving readable foreground colors in both themes. The separate control-boundary token keeps inputs and secondary actions visible without darkening every panel divider.

## Type and spacing

Use the system sans stack for labels, descriptions, and dates. Use monospace for IDs, parameter values, paths, scripts, and logs. Body copy starts at 15px on desktop and 16px for mobile form controls. Metadata starts at 13px. Page, section, and record titles use 28px, 18px, and 16px respectively.

The spacing scale is 4, 8, 12, 16, 24, and 32px. Desktop content gutters are 28–32px; mobile gutters are 16px, with 12px reserved for very narrow headers. Panel content uses a consistent 16–20px inset. Functional buttons have a 40px minimum height on desktop and 44px on narrow screens. Checkbox marks are 19px with a 44px label target.

## Layout and behavior

- The desktop header keeps the brand and five primary destinations on one line. Account, theme, locale, sign-out, and build identity live in an account menu. Below 980px, primary navigation opens from a single-row header menu with Escape and outside-click support.
- The jobs, queue, agent, catalog, and settings pages use one document scroll. Record rows are separated with light rules. Actions that are less common sit in native disclosure menus.
- Run history uses a 360–440px list beside its summary and log on wide screens. Narrow screens show the list, then open the selected run at `/runs/{id}`; the back link restores filters and list position.
- The log is the one bounded vertical viewer. Its height responds to viewport size, an empty log stays compact, it contains overscroll, and long unwrapped lines scroll horizontally inside the log. Polling appends new lines without replacing existing log nodes. Follow pauses when the reader scrolls away from the end; resuming Follow returns to the newest output.
- Forms use shared labels and input styles. Checkbox and radio controls keep their native compact shape. Script and YAML editors have separate minimum heights. Repeater edits capture current field values before rebuilding their rows.
- Menus and the run dialog support keyboard focus. The run dialog locks page scroll, makes the page inert, traps Tab, closes with Escape, reports request errors inside the dialog, and restores focus to its opener.
- Motion is limited to short color and disclosure transitions. Reduced-motion preferences disable transitions. Focus, selection, disabled, pending, and error states remain visible without relying on color alone.

## Accessibility checks

Check light and dark contrast on the actual rendered surface; target 4.5:1 for normal text, 3:1 for large text and control boundaries, and 3:1 for focus and selection indicators. Verify Korean and English at 320px through 1920px, at 200% zoom, with keyboard-only navigation, and with reduced motion. The full viewport must not scroll horizontally. Log selection and copy output must remain the original text.
