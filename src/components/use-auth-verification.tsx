import { useState } from 'react'
import { OrderVerification } from './order-verification'

// Each request consumes its own challenge, including unsuccessful requests.
export function useAuthVerification(action: string) {
  const [value, setValue] = useState({ ready: false, token: '' })
  const [generation, setGeneration] = useState(0)
  return {
    ready: value.ready,
    widget: <OrderVerification key={generation} action={action} label={action.endsWith("_send") ? "发送验证码前验证" : action.startsWith("customer") ? "查看订单前验证" : "登录前验证"} onChange={setValue} />,
    consume() {
      const token = value.token
      setValue({ ready: false, token: '' })
      setGeneration(v => v + 1)
      return token ? { turnstileToken: token } : {}
    },
  }
}
