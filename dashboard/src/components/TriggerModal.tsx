import { useState, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import type { Priority } from '../api/types'
import { api } from '../api/client'
import styles from './TriggerModal.module.css'

interface InputField {
  name: string
  type: string
  description: string
  default?: unknown
  required: boolean
}

interface TriggerModalProps {
  workflowName: string
  inputs: InputField[]
  onClose: () => void
}

export default function TriggerModal({
  workflowName,
  inputs,
  onClose,
}: TriggerModalProps) {
  const navigate = useNavigate()
  const [values, setValues] = useState<Record<string, string>>(() => {
    const initial: Record<string, string> = {}
    for (const input of inputs) {
      initial[input.name] = input.default != null ? String(input.default) : ''
    }
    return initial
  })
  const [priority, setPriority] = useState<Priority>('normal')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const handleFieldChange = useCallback((name: string, value: string) => {
    setValues((prev) => ({ ...prev, [name]: value }))
  }, [])

  const handleSubmit = useCallback(async () => {
    setSubmitting(true)
    setError(null)
    try {
      // Parse values based on input types.
      const variables: Record<string, unknown> = {}
      for (const input of inputs) {
        const raw = values[input.name]
        if (raw === '' && !input.required) continue
        switch (input.type) {
          case 'number':
          case 'integer':
            variables[input.name] = Number(raw)
            break
          case 'boolean':
            variables[input.name] = raw === 'true'
            break
          default:
            variables[input.name] = raw
        }
      }
      const result = await api.workflows.trigger(workflowName, variables, priority)
      onClose()
      navigate(`/instances/${result.instanceId}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Trigger failed')
    } finally {
      setSubmitting(false)
    }
  }, [workflowName, inputs, values, priority, onClose, navigate])

  return (
    <div className={styles.overlay} onClick={onClose}>
      <div className={styles.modal} onClick={(e) => e.stopPropagation()}>
        <div className={styles.header}>
          <span className={styles.headerTitle}>
            Trigger {workflowName}
          </span>
          <button className={styles.closeButton} onClick={onClose}>
            &#x2715;
          </button>
        </div>

        <div className={styles.body}>
          {inputs.length === 0 && (
            <div className={styles.emptyInputs}>
              This workflow has no input parameters.
            </div>
          )}

          {inputs.map((input) => (
            <div key={input.name} className={styles.field}>
              <label className={styles.fieldLabel}>
                {input.name}
                {input.required && (
                  <span className={styles.fieldRequired}>*</span>
                )}
              </label>
              {input.description && (
                <div className={styles.fieldDescription}>
                  {input.description}
                </div>
              )}
              {input.type === 'boolean' ? (
                <select
                  className={styles.fieldInput}
                  value={values[input.name]}
                  onChange={(e) =>
                    handleFieldChange(input.name, e.target.value)
                  }
                >
                  <option value="true">true</option>
                  <option value="false">false</option>
                </select>
              ) : (
                <input
                  className={styles.fieldInput}
                  type={input.type === 'number' || input.type === 'integer' ? 'number' : 'text'}
                  value={values[input.name]}
                  placeholder={`${input.type}${input.default != null ? ` (default: ${String(input.default)})` : ''}`}
                  onChange={(e) =>
                    handleFieldChange(input.name, e.target.value)
                  }
                />
              )}
            </div>
          ))}

          <div className={styles.field}>
            <label className={styles.fieldLabel}>Priority</label>
            <select
              className={styles.fieldInput}
              value={priority}
              onChange={(e) => setPriority(e.target.value as Priority)}
            >
              <option value="high">High</option>
              <option value="normal">Normal</option>
              <option value="low">Low</option>
            </select>
          </div>

          {error && <div className={styles.errorMessage}>{error}</div>}
        </div>

        <div className={styles.footer}>
          <button className={styles.cancelButton} onClick={onClose}>
            Cancel
          </button>
          <button
            className={styles.submitButton}
            onClick={handleSubmit}
            disabled={submitting}
          >
            {submitting ? 'Triggering...' : 'Trigger'}
          </button>
        </div>
      </div>
    </div>
  )
}
