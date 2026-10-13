/** Count actual streamed bytes, independent of a missing/false Content-Length. */
export async function boundedResponseText(response: Response, maximum = 256000): Promise<string> {
  const reader = response.body?.getReader()
  if (!reader) throw new Error('Missing response body')
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let bytes = 0, text = ''
  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) return text + decoder.decode()
      bytes += value.byteLength
      if (bytes > maximum) throw new Error('Response body too large')
      text += decoder.decode(value, { stream: true })
    }
  } catch (error) {
    await reader.cancel().catch(() => {})
    throw error
  } finally { reader.releaseLock() }
}
