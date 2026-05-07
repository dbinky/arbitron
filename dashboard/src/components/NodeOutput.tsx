import type { NodeExecution } from '../api/types'
import styles from './NodeOutput.module.css'

interface NodeOutputProps {
  execution: NodeExecution | null
}

function formatDuration(start: string, end?: string): string {
  if (!end) return 'running...'
  const ms = new Date(end).getTime() - new Date(start).getTime()
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

export default function NodeOutput({ execution }: NodeOutputProps) {
  if (!execution) {
    return <div className={styles.empty}>Click a node to see its output</div>
  }

  return (
    <div className={styles.container}>
      {/* Timing */}
      <div className={styles.section}>
        <div className={styles.sectionTitle}>Timing</div>
        <div className={styles.timing}>
          <span>
            <span className={styles.timingLabel}>Duration:</span>
            {formatDuration(execution.startedAt, execution.completedAt)}
          </span>
          <span>
            <span className={styles.timingLabel}>Attempt:</span>
            {execution.attempt}
          </span>
        </div>
      </div>

      {/* Script stdout/stderr */}
      {execution.scriptStdout != null && (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>Stdout</div>
          <pre className={styles.mono}>{execution.scriptStdout}</pre>
        </div>
      )}
      {execution.scriptStderr != null && (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>Stderr</div>
          <pre className={styles.errorBlock}>{execution.scriptStderr}</pre>
        </div>
      )}
      {execution.scriptExitCode != null && (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>Exit Code</div>
          <pre className={styles.mono}>{execution.scriptExitCode}</pre>
        </div>
      )}

      {/* LLM conversation */}
      {execution.llmConversation != null && (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>LLM Conversation</div>
          <pre className={styles.mono}>
            {JSON.stringify(execution.llmConversation, null, 2)}
          </pre>
          {(execution.llmModel || execution.llmTokensIn != null || execution.llmTokensOut != null) && (
            <div className={styles.llmMeta}>
              {execution.llmModel && (
                <span className={styles.llmMetaItem}>{execution.llmModel}</span>
              )}
              {execution.llmTokensIn != null && (
                <span className={styles.llmMetaItem}>
                  In: {execution.llmTokensIn}
                </span>
              )}
              {execution.llmTokensOut != null && (
                <span className={styles.llmMetaItem}>
                  Out: {execution.llmTokensOut}
                </span>
              )}
            </div>
          )}
        </div>
      )}

      {/* Resolved inputs */}
      {execution.resolvedInputs != null && Object.keys(execution.resolvedInputs).length > 0 && (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>Resolved Inputs</div>
          <pre className={styles.mono}>
            {JSON.stringify(execution.resolvedInputs, null, 2)}
          </pre>
        </div>
      )}

      {/* Output */}
      {execution.output != null && Object.keys(execution.output).length > 0 && (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>Output</div>
          <pre className={styles.mono}>
            {JSON.stringify(execution.output, null, 2)}
          </pre>
        </div>
      )}

      {/* Error */}
      {execution.error != null && Object.keys(execution.error).length > 0 && (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>Error</div>
          <pre className={styles.errorBlock}>
            {JSON.stringify(execution.error, null, 2)}
          </pre>
        </div>
      )}
    </div>
  )
}
