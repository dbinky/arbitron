// Entry of dashboard/render.html — the deterministic render surface driven by
// the Go browser renderer (adapter/renderreview). Not used by the dashboard UI.
import { createRoot } from 'react-dom/client'
import '../styles/theme.css'
import '../graph/arranged/fonts'
import { fetchJob } from './job'
import { runMeasureJob } from './MeasureSurface'
import { runRenderJob } from './RenderSurface'

async function boot(): Promise<void> {
  window.__ARBITRON_RENDER_STATE__ = 'loading'
  try {
    const job = await fetchJob(window.location.search)
    const root = createRoot(document.getElementById('render-root')!)
    if (job.mode === 'measure') {
      window.__ARBITRON_MEASURE_RESULT__ = await runMeasureJob(job, root, document, window)
    } else {
      window.__ARBITRON_RENDER_REPORT__ = await runRenderJob(job, root, document, window)
    }
    window.__ARBITRON_RENDER_STATE__ = 'ready'
  } catch (err) {
    window.__ARBITRON_RENDER_ERROR__ = err instanceof Error ? err.message : String(err)
    window.__ARBITRON_RENDER_STATE__ = 'error'
  }
}

void boot()
