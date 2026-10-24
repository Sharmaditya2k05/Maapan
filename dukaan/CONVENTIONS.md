# dukaan — how screens are built

Read this before adding a page. It keeps forty screens looking and behaving like one app.

## Stack and layout

- Next.js 15 App Router, React 19, TypeScript strict, Tailwind v4 (tokens in `src/app/globals.css`), lucide icons,
  TanStack Query v5, `idb` for offline storage, `qrcode` for UPI QR rendering, `recharts` for charts. **No other runtime deps.**
- Every screen is a client component (`'use client'`) under `src/app/app/<area>/…/page.tsx`; the authenticated shell,
  session guard and navigation are already there (`src/app/app/layout.tsx`, `src/components/AppShell.tsx`, `src/components/nav.ts`).
- Shared pieces live in `src/components/<area>/`; pure logic (no React) in `src/lib/<area>/` with `*.test.ts` run by
  `npm test` (Node's test runner).
- Money is **integer paise** everywhere; quantities are numbers with ≤ 3 decimals; tax rates are basis points.

## Talking to the backend

- `src/lib/api.ts`: `api/get/post/put/del` go through the dwaar gateway as `/<service>/v1/…`, add the bearer token and
  `X-Store-ID` automatically, refresh on 401 and throw `ApiError`. Never call `fetch` directly.
- `src/lib/hooks.ts`:
  - `useGet<T>(path, query)` one resource · `useList<T>(path, query)` paged `{items,total,limit,offset}` ·
    pass `null` as the path to skip (e.g. no permission, no id yet).
  - `useSave<Out, In>(path | (input) => path, { method, invalidate: ['/bahi/v1/parties'], onSuccess })` for writes;
    it toasts errors for you. `await save.mutateAsync(body)`.
  - `useSessionUser()`, `useStore()` (active store id, switch), `usePerm()` → `canDo(PERMS.x)`, `useOnline()`,
    `useDebounced()`, `useHotkey()`, `todayIST()`, `fyStartIST()`, `fmtDate()`, `fmtQty()`, `downloadFile()`.
- Shapes: `src/lib/types.ts`. When a backend type is missing, add it there (named after the Go struct) rather than
  inlining `any`. Request bodies are documented in each service's README and `internal/*/model.go`.
- Permissions only hide UI (`src/lib/rbac.ts`); the server enforces them. Gate buttons with `canDo(...)` and whole
  pages with an `Empty` state, never a crash.

## UI kit (`@/components/ui`)

`Button` (variants primary/secondary/ghost/danger/gold, `icon`, `loading`), `IconButton`, `PageHeader`, `Card`, `Badge`
+ `statusTone(status)`, `Field`/`Input`/`Textarea`/`Select`/`MoneyInput`/`SearchInput`/`Toggle`, `Empty`, `ErrorBox`,
`Spinner`, `Skeleton`, `QueryState` (loading/error/empty switch for any query), `Modal`, `Confirm`, `Tabs`, `Table`
(use `.th`/`.td`/`.num` classes), `Pagination`, `Money`, `StatCard`, `Avatar`, `Kbd`, `useToast`.
`src/lib/prefs.ts` holds per-device preferences (printer width, auto-print).

Page skeleton:

```tsx
'use client'
export default function PartiesPage() {
  const [q, setQ] = useState(''); const dq = useDebounced(q); const [offset, setOffset] = useState(0)
  const list = useList<Party>('/bahi/v1/parties', { q: dq, limit: 50, offset })
  return (<>
    <PageHeader title="Parties" actions={<Button icon={Plus} onClick={...}>Add party</Button>} />
    <Card padded={false}>
      <div className="flex gap-2 p-4"><SearchInput value={q} onChange={setQ} placeholder="Name, phone, GSTIN" /></div>
      <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}
        empty={<Empty icon={Users} title="No parties yet" />}>
        {(d) => (<><Table>…</Table><Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} /></>)}
      </QueryState>
    </Card>
  </>)
}
```

## Behaviour rules

- Lists: search box + filters on top, table on ≥ md, stacked rows on mobile where it matters (POS, parties), pagination
  at the bottom. Dates via `fmtDate`, money via `<Money>`, quantities via `fmtQty`.
- Forms: local `useState` for the draft, validate the obvious client-side, submit with `useSave`, show server messages
  from `ApiError.message` (they are written for users). Keep the primary action bottom-right; `Esc` closes modals.
- Everything that prints uses the rules in `globals.css`: wrap the printable in `.print-root` and either `.print-a4`
  or `.print-thermal` (+ `.thermal-58`); mark controls `.no-print`; call `window.print()`.
- Offline: only the POS counter must work offline (see `src/lib/offline.ts`). Everything else shows the network error.
- Keyboard: the POS keeps focus on the scan box; `F2` tenders, `F4` hold, `Esc` back. Document shortcuts with `<Kbd>`.
- Accessibility: every icon button has `aria-label`; inputs have labels via `Field`.
- No hard-coded tenant/store ids, no `any`, no `console.log` left behind; `npm run lint` (eslint + tsc) must be clean.
