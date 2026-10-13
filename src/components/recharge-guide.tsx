import { useCallback, useEffect, useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { Check, ChevronLeft, ChevronRight, ExternalLink, LockKeyhole, MousePointer2, Pause, Play, RotateCcw, Phone } from 'lucide-react'
import { ProductLogo } from './brand'
import { RechargeSceneCanvas } from './recharge-scene-canvas'
import { guideSession } from './recharge-guide-session'
import { RechargePackageScene } from './recharge-package-scene'
import { Button } from './ui/button'
import type { CatalogPlan } from '@/lib/api'
import '@/styles/recharge-guide.css'

const steps = [
  { title: '登录 ChatGPT', short: '登录', description: '打开 chatgpt.com，登录你要充值的 ChatGPT 账号。', address: 'chatgpt.com/auth/login', duration: 6200 },
  { title: '复制完整 Session', short: '复制', description: '在已登录 ChatGPT 的同一个浏览器里，打开 chatgpt.com/api/auth/session，复制页面里的所有内容。', address: 'chatgpt.com/api/auth/session', duration: 6800 },
  { title: '选择套餐并下单', short: '下单', description: '返回本站，选择需要的套餐，填写下单邮箱并创建订单。', address: '本站 / 选择套餐', duration: 9000 },
  { title: '完成支付宝付款', short: '付款', description: '在订单付款窗口核对套餐和金额，用手机支付宝扫一扫完成付款，付款确认后自动进入账号连接。', address: '本站 / 确认付款', duration: 8500 },
  { title: '付款后粘贴 Session', short: '提交', description: '完成支付宝付款，在订单中粘贴刚才复制的完整 Session，核对识别出的账号，勾选授权后提交充值。', address: '本站 / 连接账号', duration: 8500 },
  { title: '等待充值到账', short: '到账', description: '提交后等待充值结果。可以在「我的订单」查看进度，完成后前往 ChatGPT 查看订阅。', address: '本站 / 充值结果', duration: 5600 },
] as const

function SessionCode({ selected = false }: { selected?: boolean }) {
  return <pre className={`rg-code${selected ? ' rg-code-select' : ''}`}><code>{guideSession}</code></pre>
}

function GuideScene({ step, offers, elapsed, reduced, running, outgoing = false }: { step: number; offers: CatalogPlan[]; elapsed: number; reduced: boolean; running: boolean; outgoing?: boolean }) {
  return <div className={`rg-scene${outgoing ? ' rg-scene-outgoing' : ''}`} data-step={step} data-outgoing={outgoing} aria-hidden="true">
    {step === 0 ? <RechargeSceneCanvas>{() => <div className="rg-auth-page">
      <div className="rg-auth-brand">ChatGPT</div>
      <div className="rg-auth-form"><h3>登录或注册</h3>
        <div className="rg-auth-provider"><svg viewBox="0 0 24 24" width="19" height="19"><path fill="#4285F4" d="M21.6 12.23c0-.71-.06-1.39-.18-2.05H12v3.88h5.38a4.6 4.6 0 0 1-2 3.02v2.51h3.24c1.9-1.75 2.98-4.33 2.98-7.36Z"/><path fill="#34A853" d="M12 22c2.7 0 4.96-.9 6.62-2.41l-3.24-2.51c-.9.6-2.04.96-3.38.96-2.6 0-4.81-1.76-5.6-4.12H3.06v2.59A10 10 0 0 0 12 22Z"/><path fill="#FBBC05" d="M6.4 13.92a6 6 0 0 1 0-3.84V7.49H3.06a10 10 0 0 0 0 9.02l3.34-2.59Z"/><path fill="#EA4335" d="M12 5.96c1.47 0 2.79.5 3.83 1.51L18.7 4.6A9.6 9.6 0 0 0 12 2a10 10 0 0 0-8.94 5.49l3.34 2.59C7.19 7.72 9.4 5.96 12 5.96Z"/></svg>使用 Google 账户继续</div>
        <div className="rg-auth-provider"><svg viewBox="0 0 24 24" width="20" height="20" fill="currentColor"><path d="M17.05 12.54c.03 3.2 2.8 4.27 2.83 4.28-.02.07-.44 1.51-1.46 3-.88 1.29-1.79 2.57-3.23 2.6-1.41.03-1.87-.84-3.49-.84-1.61 0-2.13.81-3.47.87-1.39.05-2.44-1.39-3.33-2.67-1.81-2.62-3.2-7.4-1.34-10.62a5.16 5.16 0 0 1 4.36-2.65c1.36-.03 2.65.92 3.48.92.83 0 2.38-1.14 4.01-.97.68.03 2.61.27 3.85 2.08-.1.06-2.3 1.34-2.27 4ZM14.4 4.7c.74-.9 1.24-2.15 1.1-3.4-1.07.05-2.36.71-3.12 1.6-.69.8-1.29 2.09-1.13 3.31 1.19.09 2.41-.6 3.15-1.51Z"/></svg>使用 Apple 账户继续</div>
        <div className="rg-auth-provider"><Phone size={17} />使用电话号码继续</div>
        <div className="rg-auth-or"><span />或<span /></div>
        <div className="rg-auth-email"><span className="rg-auth-placeholder">Email address</span><span className="rg-auth-entered">alex@example.com</span></div>
        <div className="rg-auth-continue">继续</div><span className="rg-auth-try">抢先试用</span>
      </div>
      <footer className="rg-auth-footer"><ProductLogo productId="chatgpt" /><div>使用条款<span>|</span>隐私政策</div></footer>
      <MousePointer2 className="rg-cursor rg-auth-cursor" size={25} />
    </div>}</RechargeSceneCanvas> : step === 1 ? <div className="rg-session-scene">
      <SessionCode selected />
      <div className="rg-shortcut rg-select-shortcut"><kbd>Ctrl</kbd><span>+</span><kbd>A</kbd></div>
      <div className="rg-shortcut rg-copy-shortcut"><kbd>Ctrl</kbd><span>+</span><kbd>C</kbd><Check size={13} /></div>
    </div> : <RechargePackageScene offers={offers} elapsed={elapsed} reduced={reduced} running={running} view={step === 2 ? 'package' : step === 3 ? 'payment' : step === 4 ? 'session' : 'result'} />}
  </div>
}

export function RechargeGuide({ offers }: { offers: CatalogPlan[] }) {
  const root = useRef<HTMLElement>(null)
  const [step, setStep] = useState(0)
  const [run, setRun] = useState(0)
  const [playing, setPlaying] = useState(true)
  const [visible, setVisible] = useState(false)
  const [foreground, setForeground] = useState(!document.hidden)
  const [reduced, setReduced] = useState(() => matchMedia('(prefers-reduced-motion: reduce)').matches)
  const [elapsed, setElapsed] = useState(0)
  const [previous, setPrevious] = useState<{ step: number; run: number; elapsed: number } | null>(null)
  const [sceneHeight, setSceneHeight] = useState(356)
  const running = playing && visible && foreground && !reduced
  const sceneKey = (index: number, replay: number) => `${index >= 2 ? 'shop' : index}-${replay}`
  const select = useCallback((index: number) => {
    const sharedShop = index !== step && index >= 2 && step >= 2
    setPrevious(reduced || sharedShop ? null : { step, run, elapsed })
    setStep(index); setElapsed(0)
    if (!sharedShop) setRun(value => value + 1)
  }, [step, run, elapsed, reduced])
  useEffect(() => {
    if (!previous) return
    const timer = window.setTimeout(() => setPrevious(null), 700)
    return () => clearTimeout(timer)
  }, [previous])
  useLayoutEffect(() => {
    const current = root.current!.querySelector<HTMLElement>('.rg-scene[data-outgoing="false"]')!
    const measure = () => setSceneHeight(current.offsetHeight)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(current)
    return () => observer.disconnect()
  }, [step, run])
  useEffect(() => {
    const player = root.current!.querySelector('.rg-browser')!
    const updatePlayerVisibility = () => {
      const rect = player.getBoundingClientRect()
      const overlap = Math.max(0, Math.min(rect.bottom, innerHeight) - Math.max(rect.top, 0))
      setVisible(overlap >= Math.min(rect.height, innerHeight) * .2)
    }
    const observer = new IntersectionObserver(updatePlayerVisibility, { threshold: [0, .2, .35, 1] })
    observer.observe(player)
    window.addEventListener('scroll', updatePlayerVisibility, { passive: true })
    window.addEventListener('resize', updatePlayerVisibility)
    updatePlayerVisibility()
    const preference = matchMedia('(prefers-reduced-motion: reduce)')
    const updatePreference = () => setReduced(preference.matches)
    const updateVisibility = () => setForeground(!document.hidden)
    preference.addEventListener('change', updatePreference)
    document.addEventListener('visibilitychange', updateVisibility)
    return () => { observer.disconnect(); window.removeEventListener('scroll', updatePlayerVisibility); window.removeEventListener('resize', updatePlayerVisibility); preference.removeEventListener('change', updatePreference); document.removeEventListener('visibilitychange', updateVisibility) }
  }, [])
  useEffect(() => {
    if (!running) return
    let last = performance.now()
    const timer = window.setInterval(() => {
      const now = performance.now()
      const delta = now - last
      last = now
      setElapsed(value => Math.min(value + delta, steps[step].duration))
    }, 100)
    return () => clearInterval(timer)
  }, [running, step, run])
  useEffect(() => {
    if (elapsed < steps[step].duration) return
    const frame = requestAnimationFrame(() => {
      if (step === steps.length - 1) setPlaying(false)
      else select(step + 1)
    })
    return () => cancelAnimationFrame(frame)
  }, [elapsed, step, select])
  const replay = () => { select(0); setPlaying(true) }
  const toggle = () => { if (step === steps.length - 1 && elapsed >= steps[step].duration) replay(); else setPlaying(value => !value) }
  return <section ref={root} id="how-it-works" className="recharge-guide page-width" aria-labelledby="recharge-guide-title" data-running={running} data-reduced={reduced}>
    <header className="rg-heading"><div><span className="section-kicker">从登录，到到账</span><h2 id="recharge-guide-title">ChatGPT 充值流程</h2><p>先准备完整 Session，再下单、付款并提交。</p></div><div className="rg-controls">
      {!reduced && <Button className="rg-play-toggle" variant="outline" size="icon" onClick={toggle} aria-label={playing ? '暂停流程动画' : '播放流程动画'}>{playing ? <Pause size={16} /> : <Play size={16} />}</Button>}
      <Button className="rg-replay-button" variant="ghost" onClick={replay} aria-label={reduced ? '回到第一步' : '重播'}><RotateCcw size={14} /><span>{reduced ? '回到开始' : '重播'}</span></Button>
    </div></header>
    <div className="rg-layout">
      <ol className="rg-step-list" aria-label="充值步骤">{steps.map((item, index) => <li key={item.title}><button onClick={() => select(index)} aria-current={step === index ? 'step' : undefined} className={step === index ? 'rg-active' : ''}><span className="rg-step-number">{index < step ? <Check size={15} /> : `0${index + 1}`}</span><span className="rg-step-label"><strong>{item.title}</strong><small>{index === 1 ? '使用同一个浏览器' : index === 3 ? '扫码付款，自动确认' : index === 4 ? '先付款，再提交' : `第 ${index + 1} 步`}</small></span><span className="rg-step-short">{item.short}</span><ChevronRight size={14} className="rg-step-chevron" /></button></li>)}</ol>
      <div className="rg-player">
        <div className="rg-browser"><div className="rg-browser-bar"><div className="rg-window-dots"><i /><i /><i /></div><div className="rg-address"><LockKeyhole size={11} /><span key={step}>{steps[step].address}</span></div></div>
          <div className="rg-scene-viewport" data-step={step} style={{ height: sceneHeight }}>
            {previous && <GuideScene key={sceneKey(previous.step, previous.run)} {...previous} offers={offers} reduced={reduced} running={false} outgoing />}
            <GuideScene key={sceneKey(step, run)} step={step} offers={offers} elapsed={elapsed} reduced={reduced} running={running} />
          </div>
          <div className="rg-progress"><span style={{ width: `${elapsed / steps[step].duration * 100}%` } as CSSProperties} /></div>
        </div>
        <div className="rg-caption"><div key={step} className="rg-caption-copy" aria-live={playing && !reduced ? 'off' : 'polite'}><span>0{step + 1} / 0{steps.length}</span><h3>{steps[step].title}</h3><p>{steps[step].description}</p></div><div className="rg-navigation"><Button variant="ghost" size="icon" disabled={step === 0} onClick={() => select(step - 1)} aria-label="上一步"><ChevronLeft size={18} /></Button><Button variant="ghost" size="icon" disabled={step === steps.length - 1} onClick={() => select(step + 1)} aria-label="下一步"><ChevronRight size={18} /></Button></div></div>
        {(step === 0 || step === 1) && <a className="rg-external" href={step === 0 ? 'https://chatgpt.com' : 'https://chatgpt.com/api/auth/session'} target="_blank" rel="noreferrer">{step === 0 ? '打开 ChatGPT 登录' : '在当前浏览器打开 Session 页面'}<ExternalLink size={13} /></a>}
      </div>
    </div>
    <p className="rg-scope">适用于 Plus、Pro 5×、Pro 10×、Pro 25× 自助充值。付款后提交完整 Session，开通进度以订单为准。</p>
  </section>
}
