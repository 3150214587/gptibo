export function parseFeePercent(value: string): number | null {
  const normalized = value.trim()
  if (!/^\d{1,3}(\.\d{1,2})?$/.test(normalized) || Number(normalized) > 100) return null
  return Math.round(Number(normalized) * 100)
}

// Only enabled channels are editable. Inactive channels retain their saved rates,
// including when an incomplete draft is hidden by switching or disabling a method.
export function paymentFeeDraft(channelIds: string[], enabled: string[], saved: Record<string, number>, drafts: Record<string, string>) {
  const feeRatesBps = Object.fromEntries(channelIds.map(id => [id, saved[id] ?? 0]))
  const invalidChannels: string[] = []
  for (const id of enabled) {
    const rate = parseFeePercent(drafts[id] ?? String((saved[id] ?? 0) / 100))
    if (rate === null) invalidChannels.push(id)
    else feeRatesBps[id] = rate
  }
  return { feeRatesBps, invalidChannels }
}
