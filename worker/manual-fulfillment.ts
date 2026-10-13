import type { Order } from './order-state.ts'

// Existing independently paid orders keep their original fulfillment route.
// New 25× purchases always have a supplier snapshot and use auto recharge.
export const isLegacyManualOrder = (order: Order, hasSupplier: boolean) => order.plan === 'pro-25x' && order.mode === 'live' && !order.is_test && !hasSupplier
