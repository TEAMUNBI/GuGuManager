# GuGuManager — Liquid Command Design System

> This is the source of truth for the current interface direction. It replaces the rejected editorial/workbench brief and the generated Cinzel/amber concept. Page-specific notes under `design-system/pages/` may refine layout, but must not contradict the principles, color roles, type system, or motion limits defined here.

**Product:** GuGuManager game-server operations console  
**Direction:** Liquid Command  
**Density:** Compact, operational, information-first  
**Updated:** 2026-08-09

---

## Design Thesis

GuGuManager is a dark operational canvas for observing and controlling live game-server infrastructure. The interface should feel precise, calm, and technically capable—not decorative, cinematic, or like a generic card dashboard.

Liquid glass is a functional chrome layer. Use it for navigation, the top bar, modal shells, and primary floating controls where spatial separation matters. Data, forms, tables, logs, editors, and other content surfaces remain stable, dark, and highly legible.

The first viewport should answer three questions immediately:

1. Is the system healthy?
2. Which server, node, or task needs attention?
3. What is the next safe action?

---

## Core Principles

- **Operational clarity first.** State, exceptions, capacity, and available actions outrank decoration.
- **One connected workspace.** Prefer aligned rails, grouped measurements, and consistent separators over stacks of unrelated floating cards.
- **Glass has a job.** Apply translucent blur only to navigation and top-level spatial surfaces; nested content uses quiet tint and borders without repeated blur.
- **Motion conveys atmosphere or state.** Background flow is slow and peripheral. Interactive feedback is brief and never shifts layout.
- **Color is semantic.** Mint is primary/action, blue is informational, coral/red is critical, and amber is warning. Do not use these colors as arbitrary decoration.
- **Real data stays real.** Do not invent decorative charts or animated telemetry when the API does not provide a time series.

---

## Color System

### Foundation

| Role | Value | Purpose |
|---|---:|---|
| Canvas | `#03070B` | Deep page background |
| Raised canvas | `#071019` | Navigation and top-level depth |
| Stable surface | `#0B1620` | Data panels and grouped content |
| Elevated surface | `#111F2A` | Hovered or selected content |
| Primary text | `#F2FBFC` | Titles, values, primary labels |
| Secondary text | `#AFC0C8` | Supporting copy and metadata |
| Muted text | `#718592` | De-emphasized labels only |
| Subtle edge | `rgba(212, 242, 247, 0.11)` | Internal separators |
| Strong edge | `rgba(218, 248, 251, 0.19)` | Panel boundaries and controls |

### Semantic Signals

| Role | Value | Use |
|---|---:|---|
| Primary / healthy | `#75EAD1` | Primary emphasis, healthy states, active accents |
| Primary strong | `#A8F8E8` | Filled primary controls and high-contrast highlights |
| Information | `#55A7ED` | Informational state, links, neutral live activity |
| Critical | `#FF9275` | Destructive attention and critical incidents |
| Warning | `#F4BD6A` | Degraded or caution states |
| Error | `#FF7285` | Failures and destructive confirmation |

Mint is the signature hue. Amber is a warning signal only; it is not the brand color. Indigo is not a general-purpose accent.

### Glass Recipes

- **Chrome glass:** translucent cool tint, `24–28px` backdrop blur, subtle inner highlight, restrained shadow.
- **Surface glass:** darker tint, `10–14px` blur at most, used only for a top-level panel that must separate from the ambient field.
- **Stable content:** opaque or near-opaque ink surface with a quiet border; no stacked backdrop filters.
- Maintain sufficient foreground contrast even when the ambient field passes behind glass. Never rely on blur alone to make text readable.

---

## Typography

| Role | Family | Use |
|---|---|---|
| Interface and display | **Plus Jakarta Sans Variable** | Navigation, headings, buttons, Latin UI text |
| CJK interface | **Noto Sans SC Variable** with system Japanese/Korean fallbacks | Chinese text and multilingual fallback |
| Technical | **JetBrains Mono Variable** | IDs, ports, digests, timestamps, resource values, console-adjacent metadata |

Do not use Cinzel, Josefin Sans, editorial serif display faces, or mixed luxury typography. They conflict with the operational product character.

### Type Hierarchy

- Page title: `28–32px`, weight `650–700`, compact tracking.
- Section title: `17–20px`, weight `600–700`.
- Body/control: `13–15px`, weight `450–600`.
- Labels/metadata: `11–12px`, weight `600`, restrained tracking; avoid excessive all-caps.
- Measurements: mono where alignment or scanning benefits; never use mono for long prose.

Keep line length controlled and let translated labels wrap when necessary. Never shrink Japanese, Korean, or Chinese text below readable sizes to preserve a one-line layout.

---

## Layout and Information Architecture

- Desktop uses a compact persistent sidebar, a restrained top bar, and one primary content column.
- The overview begins with health and actionable exceptions, followed by a consolidated measurement rail and operational work surfaces.
- Use aligned grid lines, shared shells, and separators to create rhythm. Avoid giving every statistic its own floating card.
- Preserve clear hierarchy across these groups:
  - **Operations:** Overview, Servers, Task history
  - **Infrastructure:** Nodes, Game templates
  - **Administration:** Users & access, Audit log
  - **Utility:** System status, language, account, sign out
- Responsive layout must remain functional at `390px`, `768px`, `1024px`, and `1440px` without horizontal overflow.
- On mobile, navigation becomes a focused drawer; primary actions remain reachable without covering page content.

---

## Component Direction

### Navigation and Top Bar

- These are the primary glass surfaces.
- Active state uses a mint signal, not a filled neon slab.
- Keep labels direct and operational; group them by the architecture above.
- Language, account, and system utilities stay visually subordinate to operational navigation.

### Buttons and Controls

- Primary button: mint-strong fill with near-black text.
- Secondary button: dark translucent fill, strong cool edge, light text.
- Destructive button: coral/error is reserved for confirmed destructive actions.
- Hover and press feedback must not change layout dimensions.
- Every icon-only control requires an accessible name and a visible focus state.

### Panels, Tables, and Lists

- Top-level panels may use a subtle surface-glass treatment.
- Rows and nested cards use stable tint, separators, and selection state—no individual blur or dramatic elevation.
- Dense data should scan vertically with aligned labels and values.
- Console and editor surfaces remain opaque enough for sustained reading.

### Status

- Pair color with text or an icon; color alone is never the status carrier.
- Healthy/online: mint.
- Informational/running: blue.
- Warning/degraded: amber.
- Failed/offline/destructive: coral or error red.
- Avoid continuous blinking. A single restrained pulse is acceptable only when it communicates genuinely live activity.

### Forms and Modals

- Inputs use stable dark fills, visible edges, and a mint focus ring.
- Modal shell may use chrome glass; the form content inside it should remain stable and readable.
- Validation text appears next to the relevant field and remains understandable in all supported languages.

---

## Motion System

### Ambient Flow

- Use **no more than two slowly moving ambient layers per view**.
- Recommended duration: `25–35s`, transform-only, low contrast, behind all application content.
- Any grain, lens, or vignette layer remains static.
- Ambient motion never responds to pointer position and never competes with live status.
- The implementation is lightweight CSS gradients and transforms. **Do not claim or introduce Three.js/WebGL.**

### Interaction Motion

| Token | Duration | Use |
|---|---:|---|
| Instant | `60ms` | Press feedback |
| Fast | `120ms` | Hover and focus color changes |
| Default | `200ms` | Small control transitions |
| Moderate | `300ms` | Drawer, popover, and modal entry |

- Use decelerating easing for entry and standard easing for state transitions.
- Prefer opacity and transform; avoid animating layout, blur, large shadows, or dense table rows.
- Do not stagger operational data on load. Information should become available without theatrical delay.
- Under `prefers-reduced-motion: reduce`, stop ambient animation and remove non-essential transitions.

---

## Accessibility and Resilience

- Maintain at least WCAG AA contrast (`4.5:1` for normal text, `3:1` for large text and meaningful UI graphics).
- Provide visible `:focus-visible` treatment for every interactive element.
- Interactive targets should be at least `44 × 44px` where space permits, especially on mobile.
- Provide a no-backdrop-filter fallback with a more opaque stable surface.
- Support increased-contrast preferences without depending on translucency.
- Keep semantic HTML, accessible names, keyboard order, and dialog focus management intact.
- Localized content must not clip, overlap the language selector, or expose untranslated interface strings.

---

## Forbidden Patterns

- Cinzel/Josefin or luxury-editorial styling.
- Amber/indigo as the primary brand pairing.
- Glass applied to every card, list row, form section, or console surface.
- Multiple nested backdrop filters.
- More than two animated ambient layers.
- Pointer-following liquid blobs, fast looping gradients, decorative status blinking, or large-scale parallax.
- Floating-card sprawl, excessive rounding, pill-shaped containers for ordinary content, or glow on every element.
- Fabricated monitoring charts or ornamental data.
- Layout-shifting hover transforms, invisible focus states, or color-only status.
- Three.js/WebGL for the ambient background.

---

## Delivery Checklist

- [ ] Navigation and top-level chrome carry the glass effect; data surfaces stay stable.
- [ ] Mint, blue, coral/error, and amber are used according to semantic roles.
- [ ] Plus Jakarta Sans, Noto Sans SC, and JetBrains Mono are loaded with sensible system fallbacks.
- [ ] At most two slow ambient layers move; reduced-motion stops them.
- [ ] No nested blur, fabricated charts, layout-shifting hover, or decorative blinking.
- [ ] Primary flows are usable with keyboard and visible focus.
- [ ] Text and UI graphics meet contrast requirements over every ambient state.
- [ ] Layout has no clipping or horizontal overflow at `390px`, `768px`, `1024px`, and `1440px`.
- [ ] Chinese, English, Japanese, and Korean labels remain readable and consistent.
- [ ] Browser fallback works without `backdrop-filter`.

