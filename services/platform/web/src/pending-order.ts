// Persist only identifiers; recharge credentials stay in the current form's memory.
export type PendingOrder = {
  merchant_order_no: string;
  product_code: string;
  idempotency_key: string;
  created_at: string;
};
const key = (user: string) => `gptibo.pending-order.${user}`;
export function pendingSnapshot(user: string): string {
  return user ? localStorage.getItem(key(user)) || "" : "";
}
export function pendingSubscribe(callback: () => void) {
  window.addEventListener("storage", callback);
  window.addEventListener("platform-pending-order", callback);
  return () => {
    window.removeEventListener("storage", callback);
    window.removeEventListener("platform-pending-order", callback);
  };
}
export function savePending(user: string, value: PendingOrder | null) {
  if (value) localStorage.setItem(key(user), JSON.stringify(value));
  else localStorage.removeItem(key(user));
  window.dispatchEvent(new Event("platform-pending-order"));
}
export function parsePending(raw: string): PendingOrder | null {
  try {
    const p = JSON.parse(raw);
    return typeof p.merchant_order_no === "string" &&
      typeof p.product_code === "string" &&
      typeof p.idempotency_key === "string"
      ? p
      : null;
  } catch {
    return null;
  }
}
