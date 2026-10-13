import { useEffect, useState } from 'react'
import { Megaphone } from 'lucide-react'
import { maintenanceEndsAt, maintenanceNotice } from '@/lib/maintenance-notice'
import './maintenance-banner.css'

export function MaintenanceBanner() {
  const [notice, setNotice] = useState(maintenanceNotice)

  useEffect(() => {
    if (Date.now() >= maintenanceEndsAt) return
    const refresh = () => setNotice(maintenanceNotice())
    const timer = window.setInterval(refresh, 1000)
    document.addEventListener('visibilitychange', refresh)
    window.addEventListener('focus', refresh)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', refresh)
      window.removeEventListener('focus', refresh)
    }
  }, [])

  return notice ? <aside className="maintenance-banner" aria-label="维护公告" role="status">
    <div className="maintenance-banner-inner">
      <Megaphone size={16} aria-hidden="true" />
      <p><strong>维护通知</strong><span>{notice}</span></p>
    </div>
  </aside> : null
}
