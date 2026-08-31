// Auto-grab slot budgets — Settings → Download → Usenet / Torrent. GET answers
// with defaults when unset: Usenet 20/5, Torrent 0/5, where a torrent perCycle
// of 0 means torrent auto-grabs share the Usenet cycle budget.

import { api } from "./client";

export type AutoGrabSlots = {
  perCycle: number;
  perSeries: number;
};

export type AutoGrabSlotsProtocol = "usenet" | "torrent";

export function fetchAutoGrabSlots(
  protocol: AutoGrabSlotsProtocol,
): Promise<AutoGrabSlots> {
  return api<AutoGrabSlots>(`/api/settings/${protocol}-autograb-slots`);
}

export function putAutoGrabSlots(
  protocol: AutoGrabSlotsProtocol,
  slots: AutoGrabSlots,
): Promise<void> {
  return api<void>(`/api/settings/${protocol}-autograb-slots`, {
    method: "PUT",
    body: JSON.stringify(slots),
  });
}
