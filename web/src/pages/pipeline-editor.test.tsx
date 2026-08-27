import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import { PipelineEditorPage } from './pipeline-editor'

test('pipeline editor has an accessible source label and validation action', () => {
  const client = new QueryClient()
  render(<QueryClientProvider client={client}><PipelineEditorPage /></QueryClientProvider>)
  expect(screen.getByLabelText('Pipeline YAML')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '校验并编译' })).toBeEnabled()
})
