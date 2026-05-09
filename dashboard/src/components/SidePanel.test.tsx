import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import SidePanel from './SidePanel'
import type { NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'

// ---------------------------------------------------------------------------
// Mock heavy child components to focus on SidePanel's own logic
// ---------------------------------------------------------------------------

vi.mock('./NodeOutput', () => ({
  default: ({ execution }: { execution: NodeExecution | null }) => (
    <div data-testid="node-output">{execution ? 'has-execution' : 'no-execution'}</div>
  ),
}))

vi.mock('./VariablesPanel', () => ({
  default: ({ variables }: { variables: Record<string, unknown> | null }) => (
    <div data-testid="variables-panel">{variables ? 'has-variables' : 'no-variables'}</div>
  ),
}))

vi.mock('./InteractiveTranscript', () => ({
  default: ({ messages }: { messages: unknown[] | null }) => (
    <div data-testid="transcript">{messages ? `${messages.length} messages` : 'no-messages'}</div>
  ),
}))

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeExecution(overrides: Partial<NodeExecution> = {}): NodeExecution {
  return {
    id: 'exec-1',
    instanceId: 'inst-1',
    nodeId: 'node-1',
    attempt: 1,
    status: 'succeeded',
    startedAt: new Date().toISOString(),
    ...overrides,
  }
}

function makeWorkflowDef(nodeType: string, nodeConfig: Record<string, unknown> = {}): WorkflowDefinition {
  return {
    name: 'test-wf',
    description: '',
    author: '',
    nodes: [
      {
        id: 'node-1',
        type: nodeType,
        label: 'Test Node',
        description: '',
        config: nodeConfig,
      },
    ],
    edges: [],
  }
}

const interactiveMessages = [
  { role: 'user' as const, content: 'Hello' },
  { role: 'assistant' as const, content: 'Hi there' },
]

// ---------------------------------------------------------------------------
// Happy path: default rendering
// ---------------------------------------------------------------------------

describe('SidePanel — happy path', () => {
  it('shows Node Output tab by default', () => {
    render(<SidePanel execution={null} variables={null} />)
    expect(screen.getByRole('button', { name: 'Node Output' })).toBeInTheDocument()
    expect(screen.getByTestId('node-output')).toBeInTheDocument()
  })

  it('shows Variables tab', () => {
    render(<SidePanel execution={null} variables={null} />)
    expect(screen.getByRole('button', { name: 'Variables' })).toBeInTheDocument()
  })

  it('switches to Variables tab on click', () => {
    render(<SidePanel execution={null} variables={{ x: 1 }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Variables' }))
    expect(screen.getByTestId('variables-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('node-output')).not.toBeInTheDocument()
  })

  it('switches back to Node Output tab from Variables', () => {
    render(<SidePanel execution={null} variables={null} />)
    fireEvent.click(screen.getByRole('button', { name: 'Variables' }))
    fireEvent.click(screen.getByRole('button', { name: 'Node Output' }))
    expect(screen.getByTestId('node-output')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Transcript extraction: output.interactiveMessages path
// ---------------------------------------------------------------------------

describe('SidePanel — extractTranscript via output.interactiveMessages', () => {
  it('shows Transcript tab when output.interactiveMessages is present', () => {
    const execution = makeExecution({
      output: { interactiveMessages: interactiveMessages },
    })
    render(<SidePanel execution={execution} variables={null} selectedNodeId="node-1" />)
    expect(screen.getByRole('button', { name: 'Transcript' })).toBeInTheDocument()
  })

  it('auto-switches to Transcript tab when interactiveMessages found', () => {
    const execution = makeExecution({
      output: { interactiveMessages: interactiveMessages },
    })
    render(<SidePanel execution={execution} variables={null} selectedNodeId="node-1" />)
    expect(screen.getByTestId('transcript')).toBeInTheDocument()
    expect(screen.queryByTestId('node-output')).not.toBeInTheDocument()
  })

  it('passes correct message count to InteractiveTranscript', () => {
    const execution = makeExecution({
      output: { interactiveMessages: interactiveMessages },
    })
    render(<SidePanel execution={execution} variables={null} selectedNodeId="node-1" />)
    expect(screen.getByText('2 messages')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Transcript extraction: llmConversation.messages object path
// ---------------------------------------------------------------------------

describe('SidePanel — extractTranscript via llmConversation.messages', () => {
  it('shows Transcript tab when llmConversation has messages array', () => {
    const execution = makeExecution({
      llmConversation: { messages: interactiveMessages },
    })
    render(<SidePanel execution={execution} variables={null} selectedNodeId="node-1" />)
    expect(screen.getByRole('button', { name: 'Transcript' })).toBeInTheDocument()
  })

  it('auto-switches to Transcript tab for llmConversation.messages', () => {
    const execution = makeExecution({
      llmConversation: { messages: interactiveMessages },
    })
    render(<SidePanel execution={execution} variables={null} selectedNodeId="node-1" />)
    expect(screen.getByTestId('transcript')).toBeInTheDocument()
    expect(screen.getByText('2 messages')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Transcript extraction: direct llmConversation array path
// ---------------------------------------------------------------------------

describe('SidePanel — extractTranscript via llmConversation direct array', () => {
  it('shows Transcript tab when llmConversation is a direct array of messages', () => {
    const execution = makeExecution({
      llmConversation: interactiveMessages as unknown as Record<string, unknown>,
    })
    render(<SidePanel execution={execution} variables={null} selectedNodeId="node-1" />)
    expect(screen.getByRole('button', { name: 'Transcript' })).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Interactive node detection: shows Transcript tab even without transcript data
// ---------------------------------------------------------------------------

describe('SidePanel — interactive node detection', () => {
  it('shows Transcript tab for node.type === "interactive"', () => {
    const wf = makeWorkflowDef('interactive')
    render(
      <SidePanel
        execution={null}
        variables={null}
        selectedNodeId="node-1"
        workflowDef={wf}
      />,
    )
    expect(screen.getByRole('button', { name: 'Transcript' })).toBeInTheDocument()
  })

  it('shows Transcript tab for node.type === "interactive_call"', () => {
    const wf = makeWorkflowDef('interactive_call')
    render(
      <SidePanel
        execution={null}
        variables={null}
        selectedNodeId="node-1"
        workflowDef={wf}
      />,
    )
    expect(screen.getByRole('button', { name: 'Transcript' })).toBeInTheDocument()
  })

  it('shows Transcript tab when node.config.interactive === true', () => {
    const wf = makeWorkflowDef('custom', { interactive: true })
    render(
      <SidePanel
        execution={null}
        variables={null}
        selectedNodeId="node-1"
        workflowDef={wf}
      />,
    )
    expect(screen.getByRole('button', { name: 'Transcript' })).toBeInTheDocument()
  })

  it('does not show Transcript tab for a non-interactive node without transcript', () => {
    const wf = makeWorkflowDef('llm')
    render(
      <SidePanel
        execution={makeExecution()}
        variables={null}
        selectedNodeId="node-1"
        workflowDef={wf}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Transcript' })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Edge cases: null/missing data
// ---------------------------------------------------------------------------

describe('SidePanel — edge cases', () => {
  it('does not show Transcript tab when execution is null', () => {
    render(<SidePanel execution={null} variables={null} />)
    expect(screen.queryByRole('button', { name: 'Transcript' })).not.toBeInTheDocument()
  })

  it('does not show Transcript tab when execution has empty interactiveMessages', () => {
    const execution = makeExecution({ output: { interactiveMessages: [] } })
    render(<SidePanel execution={execution} variables={null} />)
    expect(screen.queryByRole('button', { name: 'Transcript' })).not.toBeInTheDocument()
  })

  it('renders without crashing when no props are passed', () => {
    render(<SidePanel execution={null} variables={null} />)
    expect(screen.getByTestId('node-output')).toBeInTheDocument()
  })

  it('falls back to output tab when transcript tab is removed (node deselected)', () => {
    const wf = makeWorkflowDef('interactive')
    const { rerender } = render(
      <SidePanel
        execution={null}
        variables={null}
        selectedNodeId="node-1"
        workflowDef={wf}
      />,
    )
    // Transcript tab visible for interactive node
    expect(screen.getByRole('button', { name: 'Transcript' })).toBeInTheDocument()

    // Deselect node — no workflowDef context, no transcript
    rerender(<SidePanel execution={null} variables={null} selectedNodeId={null} />)
    expect(screen.queryByRole('button', { name: 'Transcript' })).not.toBeInTheDocument()
    expect(screen.getByTestId('node-output')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Boundary: clicking Transcript tab then switching away and back
// ---------------------------------------------------------------------------

describe('SidePanel — tab interaction boundary', () => {
  it('can switch between Output and Transcript tabs', () => {
    const execution = makeExecution({
      output: { interactiveMessages: interactiveMessages },
    })
    render(<SidePanel execution={execution} variables={null} selectedNodeId="node-1" />)
    // Starts on transcript (auto-switched)
    expect(screen.getByTestId('transcript')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Node Output' }))
    expect(screen.getByTestId('node-output')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Transcript' }))
    expect(screen.getByTestId('transcript')).toBeInTheDocument()
  })
})
