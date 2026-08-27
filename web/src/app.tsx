import { createBrowserRouter, RouterProvider } from 'react-router-dom'
import { Layout } from './layout'
import { DashboardPage } from './pages/dashboard'
import { PipelineEditorPage } from './pages/pipeline-editor'

const router = createBrowserRouter([
  { path: '/', element: <Layout />, children: [
    { index: true, element: <DashboardPage /> },
    { path: 'pipelines/new', element: <PipelineEditorPage /> },
  ] },
])

export function App() { return <RouterProvider router={router} /> }
