import { useEffect, useRef, useState } from 'react'
import { Group, Panel, Separator, usePanelRef } from 'react-resizable-panels'
import * as Tooltip from '@radix-ui/react-tooltip'
import { Toaster } from 'sonner'
import { Events, Window } from '@wailsio/runtime'
import { useStore } from './lib/store'
import { useThemeSync } from './lib/theme'
import { cn, layoutKeys, readLocal, writeLocal } from './lib/utils'
import { toggleSidebar, useLayout } from './lib/ui'
import { Queries, Transfers, type HealthEvent, type RetryEvent, type TransferJob } from './lib/api'
import { Transfers as DocTransfers, type TransferJob as DocTransferJob } from './lib/shell/api'
import { onDocTransferEvent } from './components/shell/ShellTransferTab'
import { Sidebar } from './components/Sidebar'
import { Workspace } from './components/Workspace'
import { ConnectionDialog } from './components/ConnectionDialog'
import { ConfirmHost, confirmAction } from './components/Confirm'
import { onTransferEvent } from './components/TransferTab'

const SIDEBAR_ANIMATION_MS = 220
const SIDEBAR_MIN_PX = 200
const SIDEBAR_DEFAULT_PX = 280

export default function App() {
  const dark = useThemeSync()
  // The sidebar keeps its width in pixels, so resizing the window does not change it.
  const sidebarPx = useRef(Math.max(SIDEBAR_MIN_PX, readLocal(layoutKeys.sidebarPx, SIDEBAR_DEFAULT_PX)))
  const sidebar = usePanelRef()
  // While the sidebar animates open or closed; dragging the separator stays instant.
  const [animating, setAnimating] = useState(false)
  const animTimer = useRef<ReturnType<typeof setTimeout>>(undefined)
  const animatingRef = useRef(false)

  // Let the rest of the app collapse and expand the sidebar, and restore its last state.
  useEffect(() => {
    const apply = (collapsed: boolean) => {
      const panel = sidebar.current
      const group = document.getElementById('main')
      if (!panel || !group) return
      clearTimeout(animTimer.current)
      animatingRef.current = true
      setAnimating(true)
      animTimer.current = setTimeout(() => {
        animatingRef.current = false
        setAnimating(false)
        // The tab strip makes room for the window buttons once the sidebar is gone.
        if (collapsed) useLayout.getState().sidebarChanged(true)
      }, SIDEBAR_ANIMATION_MS + 40)
      // The sidebar content keeps its full width while the panel animates, so it slides.
      const width = (px: number) => group.style.setProperty('--sidebar-width', `${Math.round(px)}px`)
      if (collapsed) {
        width(panel.getSize().inPixels)
        panel.collapse()
      } else {
        panel.expand()
        // Back to the remembered width (a sidebar that started collapsed has no earlier size).
        panel.resize(`${Math.round(sidebarPx.current)}px`)
        if (panel.isCollapsed()) panel.resize(`${SIDEBAR_DEFAULT_PX}px`)
        width((panel.getSize().asPercentage * group.offsetWidth) / 100)
        useLayout.getState().sidebarChanged(panel.isCollapsed())
      }
    }
    useLayout.setState({ setSidebar: apply })
    const offToggle = Events.On('view:toggle-sidebar', () => toggleSidebar())
    // Track full screen, where macOS hides the window buttons.
    const setFullscreen = (fullscreen: boolean) => useLayout.setState({ fullscreen })
    void Window.IsFullscreen()
      .then(setFullscreen)
      .catch(() => {})
    const offFull = [
      Events.On('mac:WindowWillEnterFullScreen', () => setFullscreen(true)),
      Events.On('common:WindowFullscreen', () => setFullscreen(true)),
      Events.On('mac:WindowWillExitFullScreen', () => setFullscreen(false)),
      Events.On('common:WindowUnFullscreen', () => setFullscreen(false)),
    ]
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'b') {
        e.preventDefault()
        toggleSidebar()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => {
      offToggle()
      offFull.forEach((off) => off())
      window.removeEventListener('keydown', onKey)
    }
  }, [sidebar])

  useEffect(() => {
    void useStore.getState().loadDrivers()
    void useStore.getState().loadProfiles()
    const offHealth = Events.On('session:health', (e: { data: HealthEvent }) => useStore.getState().setHealth(e.data.sessionId, e.data.health))
    const offRetry = Events.On('op:retry', (e: { data: RetryEvent }) => useStore.getState().onRetry(e.data.opId, e.data.attempt, e.data.error))
    const offTransfer = Events.On('transfer:progress', (e: { data: TransferJob }) => onTransferEvent(e.data))
    const offDocTransfer = Events.On('doc-transfer:progress', (e: { data: DocTransferJob }) => onDocTransferEvent(e.data))
    // Jobs outlive a reload of the page (not of the app).
    void Transfers.Jobs().then((jobs) => (jobs ?? []).forEach((j) => useStore.getState().setJob(j)))
    void DocTransfers.Jobs().then((jobs) => (jobs ?? []).forEach((j) => useStore.getState().setDocJob(j)))
    // Quitting with open transactions asks first; they are rolled back on exit.
    let asking = false
    const offQuit = Events.On('app:confirm-quit', async (e: { data: string[] | null }) => {
      const ids = e.data ?? []
      if (asking) return
      asking = true
      const tabs = useStore.getState().tabs.filter((t) => ids.includes(t.id))
      const ok = await confirmAction({
        title: 'Quit with open transactions?',
        message: `${tabs.map((t) => t.title || t.database || 'SQL').join(', ') || `${ids.length} tabs`} ${ids.length === 1 ? 'has a transaction' : 'have transactions'} in progress. Quitting rolls ${ids.length === 1 ? 'it' : 'them'} back.`,
        confirmLabel: 'Roll back and quit',
        danger: true,
      })
      asking = false
      if (ok) void Queries.Quit()
    })
    // Keep the WebView from navigating when a file is dropped on it.
    const prevent = (e: DragEvent) => e.preventDefault()
    window.addEventListener('dragover', prevent)
    window.addEventListener('drop', prevent)
    return () => {
      offHealth()
      offRetry()
      offTransfer()
      offDocTransfer()
      offQuit()
      window.removeEventListener('dragover', prevent)
      window.removeEventListener('drop', prevent)
    }
  }, [])

  return (
    <Tooltip.Provider delayDuration={500} skipDelayDuration={200}>
      <Group
        orientation="horizontal"
        id="main"
        className={cn('h-full', animating && 'sidebar-animating')}
        onLayoutChanged={(_, meta) => meta.isUserInteraction && writeLocal(layoutKeys.sidebarPx, Math.round(sidebarPx.current))}
      >
        <Panel
          id="sidebar"
          defaultSize={useLayout.getState().sidebarCollapsed ? 0 : `${sidebarPx.current}px`}
          minSize={`${SIDEBAR_MIN_PX}px`}
          maxSize="45%"
          groupResizeBehavior="preserve-pixel-size"
          collapsible
          collapsedSize={0}
          panelRef={sidebar}
          onResize={(size) => {
            // Keep the expanded width. While animating, the panel reports in-between widths,
            // and a collapsed one reports 0; neither is a width to come back to.
            if (!animatingRef.current && size.inPixels >= SIDEBAR_MIN_PX) sidebarPx.current = size.inPixels
            // Toggles report through apply(); this catches dragging the sidebar shut or open.
            if (!animatingRef.current) useLayout.getState().sidebarChanged(size.inPixels === 0)
          }}
        >
          <Sidebar />
        </Panel>
        <Separator className="resize-handle is-col" />
        <Panel id="workspace" minSize="480px">
          <Workspace />
        </Panel>
      </Group>
      <ConnectionDialog />
      <ConfirmHost />
      <Toaster theme={dark ? 'dark' : 'light'} position="bottom-right" richColors closeButton toastOptions={{ className: 'text-[13px]' }} />
    </Tooltip.Provider>
  )
}
