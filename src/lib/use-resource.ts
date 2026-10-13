import { useEffect, useState } from 'react'
import { api } from './api'
import { failedQuery, type QueryResult } from './query-state'

// A refresh keeps this query's content. A different query never borrows old rows.
export function useResource<T>(path: string, revision = 0, { retainOnError = false }: { retainOnError?: boolean } = {}) {
  const [result, setResult] = useState<QueryResult<T>>()
  const sameQuery = result?.path === path
  const loading = !sameQuery || result.revision !== revision
  useEffect(() => {
    const controller = new AbortController()
    api<T>(path, undefined, controller.signal).then(data => {
      if (!controller.signal.aborted) setResult({ path, revision, data, error: '' })
    }).catch(e => {
      if (!controller.signal.aborted) setResult(previous => failedQuery(previous, path, revision, e, retainOnError))
    })
    return () => controller.abort()
  }, [path, revision, retainOnError])
  return { data: sameQuery ? result.data : undefined, error: loading ? '' : result.error, loading, stale: Boolean(sameQuery && result.data !== undefined && result.error) }
}
