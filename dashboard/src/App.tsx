import { BrowserRouter, Routes, Route } from 'react-router-dom'
import NavBar from './components/NavBar'
import QueueView from './routes/QueueView'
import ExecutionView from './routes/ExecutionView'
import WorkflowsCatalog from './routes/WorkflowsCatalog'
import WorkflowReview from './routes/WorkflowReview'
import Settings from './routes/Settings'
import NotFound from './routes/NotFound'

export default function App() {
  return (
    <BrowserRouter>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
        <NavBar />
        <main style={{ flex: 1, overflow: 'auto' }}>
          <Routes>
            <Route path="/" element={<QueueView />} />
            <Route path="/instances/:id" element={<ExecutionView />} />
            <Route path="/workflows" element={<WorkflowsCatalog />} />
            <Route path="/workflows/:name" element={<WorkflowReview />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="*" element={<NotFound />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  )
}
