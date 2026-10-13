// Preserve stored audit facts while updating the presentation of old system copy.
const legacyMessages: Record<string, string> = {
  '创建服务端演示订单': '创建历史测试记录',
  '模拟付款已确认': '测试状态已更新（无实际收款）',
  '获取示例手机号': '写入测试号码（不提供接码服务）',
  '提交演示任务（成功场景）': '提交内部任务（成功场景）',
  '提交演示任务（失败场景）': '提交内部任务（失败场景）',
  '重新开启演示任务': '重新开启内部任务',
  '演示结果，无真实支付或履约': '历史测试记录已结束，无支付或履约',
}
export function activityCopy(note: string) {
  if (Object.hasOwn(legacyMessages, note)) return legacyMessages[note]
  if (note.startsWith('模拟退款完成：')) return '历史售后记录已结束（无资金往来）：' + note.slice('模拟退款完成：'.length)
  return note
}
