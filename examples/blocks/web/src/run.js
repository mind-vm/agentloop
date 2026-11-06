// streamRun posts one message and calls onEvent for every agentrun event.
//
// EventSource cannot POST, and a run's message does not belong in a URL, so
// the stream is read from fetch by hand. Frames are `event:` + `data:`
// pairs separated by a blank line; only `data:` is needed, because the JSON
// carries its own type.
export async function streamRun({ message, sessionId, signal }, onEvent) {
  const res = await fetch('/api/run', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message, sessionId }),
    signal,
  });
  if (!res.ok || !res.body) throw new Error(`${res.status} ${await res.text()}`);
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += value;
    let i;
    while ((i = buf.indexOf('\n\n')) >= 0) {
      const frame = buf.slice(0, i);
      buf = buf.slice(i + 2);
      const data = frame.split('\n').filter((l) => l.startsWith('data: ')).map((l) => l.slice(6)).join('\n');
      if (data) onEvent(JSON.parse(data));
    }
  }
}

// blockOf returns the render block an event carries, or null.
export function blockOf(e) {
  if (e.type !== 'sandbox_event' || e.args?.kind !== 'block') return null;
  return { type: e.args.summary, payload: e.args.payload ?? {} };
}
