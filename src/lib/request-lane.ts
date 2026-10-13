// Background reads yield to user actions. A click during a poll is queued,
// never silently discarded; one completion cannot unlock a newer request.
export function requestLane() {
  let tail: Promise<unknown> = Promise.resolve()
  let pending = 0
  return {
    run<T>(work: () => Promise<T>, background = false): Promise<T | undefined> {
      if (background && pending > 0) return Promise.resolve(undefined)
      pending++
      const result = tail.then(work)
      tail = result.catch(() => {}).finally(() => { pending-- })
      return result
    },
  }
}
