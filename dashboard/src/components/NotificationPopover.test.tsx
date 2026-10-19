import { render, screen } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import NotificationPopover from './NotificationPopover'
import type { NotificationItem } from '../api/types'

function item(type: string): NotificationItem {
  return {
    id: 'n-1',
    instanceId: '0f0e0d0c-0000-0000-0000-000000000001',
    workflowName: 'gathering-wf',
    type,
    read: false,
    dismissed: false,
    createdAt: new Date().toISOString(),
  }
}

function renderWith(items: NotificationItem[]) {
  return render(
    <NotificationPopover
      items={items}
      dismissedItems={[]}
      showDismissed={false}
      hasMoreDismissed={false}
      onItemClick={vi.fn()}
      onDismiss={vi.fn()}
      onShowDismissed={vi.fn()}
      onLoadMore={vi.fn()}
    />,
  )
}

describe('NotificationPopover — conflict_resolution', () => {
  it('renders a distinct icon for a gather conflict-resolution notification', () => {
    renderWith([item('conflict_resolution')])
    expect(screen.getByText('⇄')).toBeInTheDocument()
    expect(screen.getByText(/gathering-wf/)).toBeInTheDocument()
  })

  it('still falls back to the bell for unknown types', () => {
    renderWith([item('something_new')])
    expect(screen.getByText('🔔')).toBeInTheDocument()
  })
})
