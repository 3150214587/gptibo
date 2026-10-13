import { useEffect, useRef, useState } from 'react'
import { Bell, Volume2, VolumeX } from 'lucide-react'
import { api, errorMessage } from '@/lib/api'
import { Button } from '@/components/ui/button'
import '@/components/ticket-workspace.css'

type Summary = { version: number; pending: number }

export function TicketAlerts({ onOpen, onRefunds }: { onOpen: () => void; onRefunds: () => void }) {
  const [summary, setSummary] = useState<Summary>()
  const [refunds, setRefunds] = useState<Summary>()
  const [enabled, setEnabled] = useState(false)
  const [audioError, setAudioError] = useState('')
  const [errors, setErrors] = useState({ tickets: '', refunds: '' })
  const [unseen, setUnseen] = useState(false)
  const [unseenRefund, setUnseenRefund] = useState(false)
  const audio = useRef<AudioContext | null>(null)
  const speech = useRef<SpeechSynthesisUtterance | null>(null)
  const sound = useRef(false)
  const mounted = useRef(false)
  const versions = useRef<Partial<Record<'tickets' | 'refunds', number>>>({})
  function stopSpeech() {
    if (speech.current) {
      speech.current.onerror = null
      speech.current = null
      window.speechSynthesis.cancel()
    }
  }
  function audioFailed(message: string) {
    if (!mounted.current) return
    sound.current = false; setEnabled(false); setAudioError(message)
    stopSpeech()
  }
  function announceRefund() {
    if (!('speechSynthesis' in window) || !('SpeechSynthesisUtterance' in window)) throw new Error('speech unavailable')
    stopSpeech()
    const utterance = new SpeechSynthesisUtterance('您有一单退款待处理')
    utterance.lang = 'zh-CN'
    const voices = window.speechSynthesis.getVoices()
    const voice = voices.find(v => /^zh[-_]CN$/i.test(v.lang)) || voices.find(v => /^zh/i.test(v.lang))
    if (voice) utterance.voice = voice
    utterance.onend = () => { if (speech.current === utterance) speech.current = null }
    utterance.onerror = event => {
      if (speech.current === utterance && event.error !== 'canceled' && event.error !== 'interrupted') audioFailed('退款语音播放失败，请检查设备中文语音和浏览器声音权限后重新开启。')
    }
    speech.current = utterance
    window.speechSynthesis.resume()
    window.speechSynthesis.speak(utterance)
  }
  async function chime() {
    const context = audio.current
    if (!context) return
    await context.resume()
    if (!sound.current) return
    if (context.state !== 'running') throw new Error('audio blocked')
    for (const [offset, frequency] of [[0, 660], [0.22, 880]]) {
      const oscillator = context.createOscillator()
      const gain = context.createGain()
      const start = context.currentTime + offset
      oscillator.frequency.value = frequency
      gain.gain.setValueAtTime(0, start)
      gain.gain.linearRampToValueAtTime(0.16, start + 0.02)
      gain.gain.exponentialRampToValueAtTime(0.001, start + 0.3)
      oscillator.connect(gain); gain.connect(context.destination)
      oscillator.start(start); oscillator.stop(start + 0.32)
      oscillator.onended = () => { oscillator.disconnect(); gain.disconnect() }
    }
  }
  useEffect(() => {
    mounted.current = true
    let stopped = false
    const controller = new AbortController()
    const timers: ReturnType<typeof setTimeout>[] = []
    async function poll(kind: 'tickets' | 'refunds') {
      try {
        const next = await api<Summary>(`/api/admin/${kind}/notifications`, undefined, controller.signal)
        if (stopped) return
        const previous = versions.current[kind]
        const changed = previous !== undefined && next.version > previous
        versions.current[kind] = next.version
        if (kind === 'tickets') setSummary(next)
        else setRefunds(next)
        setErrors(current => ({ ...current, [kind]: '' }))
        if (changed) {
          if (kind === 'tickets') setUnseen(true)
          else setUnseenRefund(true)
          if (sound.current) {
            try {
              if (kind === 'refunds') announceRefund()
              else await chime()
            } catch { if (!stopped) audioFailed('声音播放失败，请检查浏览器声音权限后重新开启。') }
          }
        }
      } catch (e) { if (!stopped) setErrors(current => ({ ...current, [kind]: errorMessage(e) })) }
      finally { if (!stopped) timers[kind === 'tickets' ? 0 : 1] = setTimeout(() => { void poll(kind) }, 15000) }
    }
    void poll('tickets')
    void poll('refunds')
    return () => {
      stopped = true; mounted.current = false; controller.abort(); timers.forEach(clearTimeout)
      sound.current = false; stopSpeech(); void audio.current?.close(); audio.current = null
    }
  }, [])
  function toggle() {
    if (sound.current) { sound.current = false; setEnabled(false); stopSpeech(); return }
    try {
      audio.current ??= new AudioContext()
      sound.current = true; setEnabled(true); setAudioError('')
      // Start speech within the click gesture so browsers can unlock playback.
      announceRefund()
      void chime().catch(() => audioFailed('无法播放提示音，请检查浏览器声音权限后重试。'))
    } catch { audioFailed('无法播放声音，请检查浏览器是否支持中文语音并允许声音后重试。') }
  }
  const connectionError = [errors.tickets && `工单提醒连接中断：${errors.tickets}`, errors.refunds && `退款提醒连接中断：${errors.refunds}`].filter(Boolean).join('；')
  return <section className="ticket-alert" aria-label="工单与退款提醒"><div><strong><Bell size={15} className="inline" /> {unseen ? '收到新的代理工单消息' : summary ? `待处理工单 ${summary.pending} 条` : '工单提醒'} · {unseenRefund ? '收到新的退款申请' : refunds ? `待处理退款 ${refunds.pending} 单` : '退款提醒'}</strong><p role="status">{connectionError || audioError || (enabled ? '声音已开启 · 工单响铃、退款语音播报；保持后台打开，约 15 秒检查一次。' : '点击开启并试听：工单响铃，退款播报「您有一单退款待处理」。')}</p></div><div className="ticket-alert-actions"><Button variant="outline" size="sm" onClick={toggle}>{enabled ? <VolumeX size={14} /> : <Volume2 size={14} />}{enabled ? '关闭声音' : '开启声音提醒'}</Button><Button size="sm" onClick={() => { setUnseen(false); onOpen() }}>查看工单{summary ? ` (${summary.pending})` : ''}</Button><Button variant="outline" size="sm" onClick={() => { setUnseenRefund(false); onRefunds() }}>查看退款{refunds ? ` (${refunds.pending})` : ''}</Button></div></section>
}
