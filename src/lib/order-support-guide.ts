import { createContext, useContext, useEffect } from 'react'

export const OrderSupportGuideContext = createContext<(reason: string | null) => void>(() => {})

export function useOrderSupportGuide(reason: string | null) {
  const guide = useContext(OrderSupportGuideContext)
  useEffect(() => {
    guide(reason)
    return () => guide(null)
  }, [guide, reason])
}
