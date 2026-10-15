// formatLocalStamp turns an RFC3339/ISO UTC timestamp into a compact
// "YYYY-MM-DD HH:mm" string in the viewer's local timezone (the browser/
// device zone). Server storage stays UTC; only display converts.
//
// Claude 2026-10-15: shared local stamp for Activity + Organize logs.
// Reason: Queue Activity sliced createdAt as UTC wall time (…T08:13 →
//   "2026-10-12 08:13"), which reads wrong for anyone not on UTC.
// Troubleshooting: Activity log "wrong time" — compare stamp to device clock.
// Review if: Calendar History day buckets also switch to local dates
//   (deliberately left UTC — see calendar/History.tsx).
// Related files: screens/Activity.tsx, screens/OrganizeChrome.tsx

export function formatLocalStamp(iso: string): string {
  const raw = (iso ?? "").trim();
  if (!raw) return "";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) {
    return raw.slice(0, 16).replace("T", " ");
  }
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}`
  );
}
