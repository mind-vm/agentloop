import '@mantine/core/styles.css';
import '@mantine/charts/styles.css';

import { useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import {
  Accordion, Alert, AppShell, Badge, Button, Code, Container, Group, Loader,
  MantineProvider, Paper, Stack, Text, Textarea, Title,
} from '@mantine/core';

import { AskView } from './ask.jsx';
import { BlockView } from './blocks.jsx';
import { blockOf, streamRun } from './run.js';

const EXAMPLES = [
  'Build me a sales report.',
  'Give me an overview of orders.csv: revenue, open and overdue amounts, and a table of the overdue orders.',
  'Show revenue per month as a line chart and per region as a bar chart.',
  'Which products sell best? Show the share of revenue per product as a donut chart.',
];

// A turn is one message and everything the run streamed back for it, kept in
// arrival order so the page reads like the run did: scripts, then the
// blocks they emitted, then the answer.
function newTurn(message) {
  return { message, steps: [], asks: [], blocks: [], answer: '', error: '', done: null };
}

// placeBlock adds a block, or replaces the one with the same id. Within the
// current turn it replaces in place — a table growing as a job finds rows.
// From an earlier turn it moves here, so a revised chart shows next to the
// question that revised it rather than up the page where nobody is looking.
function placeBlock(turns, block) {
  const id = block.payload?.id;
  const last = turns.length - 1;
  if (!id) return turns.map((t, i) => (i === last ? { ...t, blocks: [...t.blocks, block] } : t));
  return turns.map((t, i) => {
    const at = t.blocks.findIndex((b) => b.payload?.id === id);
    if (i === last) {
      const blocks = [...t.blocks];
      if (at >= 0) blocks[at] = block;
      else blocks.push({ ...block, revised: turns.some((u, j) => j < last && u.blocks.some((b) => b.payload?.id === id)) });
      return { ...t, blocks };
    }
    return at >= 0 ? { ...t, blocks: t.blocks.filter((_, k) => k !== at) } : t;
  });
}

function App() {
  const [turns, setTurns] = useState([]);
  const [draft, setDraft] = useState('');
  const [running, setRunning] = useState(false);
  // The page names the session, not the server: a reply to ask() arrives
  // while the run is still streaming, before any `done` could tell us.
  const sessionId = useRef(crypto.randomUUID());

  const send = async (message) => {
    if (!message.trim() || running) return;
    setDraft('');
    setRunning(true);
    setTurns((ts) => [...ts, newTurn(message)]);
    const patch = (fn) => setTurns((ts) => ts.map((t, i) => (i === ts.length - 1 ? fn(t) : t)));
    try {
      await streamRun({ message, sessionId: sessionId.current }, (e) => {
        const block = blockOf(e);
        if (block) return setTurns((ts) => placeBlock(ts, block));
        switch (e.type) {
          case 'input_request':
            return patch((t) => ({ ...t, asks: [...t.asks, { ...e.args, message: e.content, answered: false }] }));
          case 'execute_js':
            return patch((t) => ({ ...t, steps: [...t.steps, { code: e.content, result: null }] }));
          case 'execute_js_result':
            return patch((t) => {
              const steps = [...t.steps];
              if (steps.length) steps[steps.length - 1] = { ...steps[steps.length - 1], result: e.content ?? '' };
              return { ...t, steps };
            });
          case 'response':
            return patch((t) => ({ ...t, answer: e.content ?? '' }));
          case 'error':
            return patch((t) => ({ ...t, error: e.content ?? 'error' }));
          case 'done':
            return patch((t) => ({ ...t, done: e.summary }));
        }
      });
    } catch (err) {
      patch((t) => ({ ...t, error: String(err) }));
    } finally {
      setRunning(false);
    }
  };

  const reply = async (turnIndex, requestId, value) => {
    const mark = (fields) =>
      setTurns((ts) => ts.map((t, i) => (i !== turnIndex ? t : {
        ...t, asks: t.asks.map((a) => (a.requestId === requestId ? { ...a, ...fields } : a)),
      })));
    mark({ answered: true, answer: value });
    const res = await fetch('/api/reply', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sessionId: sessionId.current, requestId, value }),
    });
    if (!res.ok) mark({ error: await res.text() });
  };

  return (
    <AppShell padding="lg" header={{ height: 56 }}>
      <AppShell.Header px="lg">
        <Group h="100%" justify="space-between">
          <Group gap="xs">
            <Title order={4}>thinking-core</Title>
            <Badge variant="light">blocks demo</Badge>
          </Group>
          <Text size="xs" c="dimmed">The agent writes JavaScript; ext.BlocksPack turns its results into these blocks.</Text>
        </Group>
      </AppShell.Header>
      <AppShell.Main>
        <Container size="md">
          <Stack gap="xl">
            {turns.length === 0 ? (
              <Paper withBorder p="lg" radius="md">
                <Text fw={600} mb={4}>Ask about the sales data</Text>
                <Text size="sm" c="dimmed" mb="md">
                  The agent can read <Code>orders.csv</Code> (24 orders, January–June 2026) and nothing else.
                </Text>
                <Stack gap="xs">
                  {EXAMPLES.map((ex) => (
                    <Button key={ex} variant="light" justify="flex-start" onClick={() => send(ex)} styles={{ label: { whiteSpace: 'normal', textAlign: 'left' } }} h="auto" py={8}>
                      {ex}
                    </Button>
                  ))}
                </Stack>
              </Paper>
            ) : null}
            {turns.map((t, i) => (
              <TurnView key={i} turn={t} live={running && i === turns.length - 1} onReply={(id, v) => reply(i, id, v)} />
            ))}
            <Group align="flex-end" gap="sm">
              <Textarea
                style={{ flex: 1 }}
                autosize
                minRows={1}
                placeholder={turns.length ? 'Follow up — the agent keeps its state…' : 'Ask something about orders.csv…'}
                value={draft}
                onChange={(e) => setDraft(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault();
                    send(draft);
                  }
                }}
                disabled={running}
              />
              <Button onClick={() => send(draft)} loading={running}>Send</Button>
            </Group>
          </Stack>
        </Container>
      </AppShell.Main>
    </AppShell>
  );
}

function TurnView({ turn, live, onReply }) {
  return (
    <Stack gap="sm">
      <Paper p="sm" radius="md" bg="blue.0" style={{ alignSelf: 'flex-end', maxWidth: '80%' }}>
        <Text size="sm">{turn.message}</Text>
      </Paper>
      {turn.steps.length ? (
        <Accordion variant="contained" radius="md" chevronPosition="left">
          {turn.steps.map((s, i) => {
            const failed = typeof s.result === 'string' && s.result.startsWith('Error');
            return (
              <Accordion.Item key={i} value={String(i)}>
                <Accordion.Control>
                  <Group gap="xs">
                    <Text size="sm">Script {i + 1}</Text>
                    {s.result === null ? <Loader size="xs" /> : failed ? <Badge color="orange" size="xs">error → retried</Badge> : <Badge color="teal" size="xs">ran</Badge>}
                  </Group>
                </Accordion.Control>
                <Accordion.Panel>
                  <Code block style={{ fontSize: 12 }}>{s.code}</Code>
                  {s.result ? <Code block mt="xs" color={failed ? 'orange.1' : undefined} style={{ fontSize: 12, whiteSpace: 'pre-wrap' }}>{s.result}</Code> : null}
                </Accordion.Panel>
              </Accordion.Item>
            );
          })}
        </Accordion>
      ) : null}
      {turn.asks.map((a) => <AskView key={a.requestId} request={a} onReply={(v) => onReply(a.requestId, v)} />)}
      {turn.blocks.map((b, i) => (
        <div key={b.payload?.id ?? i}>
          {b.revised ? <Badge size="xs" variant="light" color="grape" mb={4}>revised</Badge> : null}
          <BlockView block={b} />
        </div>
      ))}
      {turn.answer ? <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>{turn.answer}</Text> : null}
      {turn.error ? <Alert color="red" title="The run failed">{turn.error}</Alert> : null}
      {live && !turn.answer && !turn.asks.some((a) => !a.answered) ? <Group gap="xs"><Loader size="sm" type="dots" /><Text size="sm" c="dimmed">working…</Text></Group> : null}
      {turn.done ? (
        <Text size="xs" c="dimmed">
          {turn.done.steps} steps · {turn.done.tokens.prompt}+{turn.done.tokens.completion} tokens · {turn.blocks.length} blocks
        </Text>
      ) : null}
    </Stack>
  );
}

createRoot(document.getElementById('root')).render(
  <MantineProvider>
    <App />
  </MantineProvider>,
);
