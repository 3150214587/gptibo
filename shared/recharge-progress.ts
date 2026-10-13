// Public business stages only. Never forward arbitrary supplier text or identifiers.
export const RECHARGE_STAGES = {
  queued: { title: '排队处理中', description: '充值请求已受理，正在等待处理。' },
  logging_in: { title: '正在验证登录资料', description: '正在核对本次授权的账号资料。' },
  preparing_funds: { title: '正在准备充值资源', description: '登录资料已提交，正在准备本次充值。' },
  awaiting_card: { title: '正在安排充值', description: '正在等待可用的充值资源。' },
  dispatching: { title: '正在安排充值', description: '正在派发本次充值任务。' },
  processing: { title: '充值处理中', description: '充值任务正在处理，请等待结果确认。' },
  requires_action: { title: '需要进一步确认', description: '请联系下方客服核对当前任务，无需重新付款。' },
  confirming: { title: '正在确认充值结果', description: '充值结果尚未确认，请勿重复提交。' },
  review: { title: '充值结果核对中', description: '当前任务需要进一步核对，请保留此订单。' },
  upgrading: { title: '正在升级订阅', description: '基础步骤已完成，升级仍在处理中。' },
  completed: { title: '充值已完成', description: '充值服务已确认完成。' },
  failed: { title: '充值未完成', description: '请查看失败原因和当前可用的处理方式。' },
  cancelled: { title: '充值已取消', description: '请查看订单当前可用的处理方式。' },
} as const
export type RechargeStage = keyof typeof RECHARGE_STAGES
export function rechargeStage(value: unknown): RechargeStage | null {
  return typeof value === 'string' && Object.hasOwn(RECHARGE_STAGES, value) ? value as RechargeStage : null
}
