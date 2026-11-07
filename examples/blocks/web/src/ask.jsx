// The page's half of ask(): a script is blocked on the server until this
// replies. Questions are walked one step at a time, then reviewed and sent
// once — the run resumes the moment the reply lands.
import { useState } from 'react';
import {
  Badge, Button, Checkbox, Group, Paper, Radio, Stack, Stepper, Text, TextInput, Textarea,
} from '@mantine/core';

export function AskView({ request, onReply }) {
  if (request.answered) return <Answered request={request} />;
  switch (request.inputType) {
    case 'questions':
      return <Questions request={request} onReply={onReply} />;
    case 'confirm':
      return (
        <Frame request={request}>
          <Group>
            <Button onClick={() => onReply(true)}>Yes</Button>
            <Button variant="default" onClick={() => onReply(false)}>No</Button>
          </Group>
        </Frame>
      );
    case 'choice':
      return (
        <Frame request={request}>
          <Group>{(request.options ?? []).map((o) => <Button key={o} variant="light" onClick={() => onReply(o)}>{o}</Button>)}</Group>
        </Frame>
      );
    default:
      return <FreeText request={request} onReply={onReply} />;
  }
}

function Frame({ request, children }) {
  return (
    <Paper withBorder radius="md" p="md" style={{ borderColor: 'var(--mantine-color-blue-4)' }}>
      <Group gap="xs" mb="sm"><Badge variant="light">The agent is asking</Badge></Group>
      {request.message ? <Text fw={600} mb="sm">{request.message}</Text> : null}
      {children}
    </Paper>
  );
}

function FreeText({ request, onReply }) {
  const [v, setV] = useState('');
  return (
    <Frame request={request}>
      <Group align="flex-end">
        <TextInput style={{ flex: 1 }} placeholder={request.placeholder} value={v} onChange={(e) => setV(e.currentTarget.value)} />
        <Button onClick={() => onReply(v)} disabled={!v.trim()}>Send</Button>
      </Group>
    </Frame>
  );
}

const OTHER = '\u0000other';

// One answer per question: a label, a list of labels for multiSelect, and
// for allowOther the typed text standing in for a label.
function Questions({ request, onReply }) {
  const qs = request.questions ?? [];
  const [step, setStep] = useState(0);
  const [picked, setPicked] = useState({}); // id -> string | string[]
  const [other, setOther] = useState({}); // id -> text

  const answerOf = (q) => {
    const p = picked[q.id];
    const o = (other[q.id] ?? '').trim();
    if (q.multiSelect) {
      const list = (p ?? []).filter((x) => x !== OTHER);
      if ((p ?? []).includes(OTHER) && o) list.push(o);
      return list;
    }
    return p === OTHER ? o : p;
  };
  const complete = (q) => {
    const a = answerOf(q);
    return q.multiSelect ? a.length > 0 : !!a;
  };
  const send = () => onReply(Object.fromEntries(qs.map((q) => [q.id, answerOf(q)])));

  return (
    <Frame request={request}>
      <Stepper active={step} onStepClick={setStep} size="sm" allowNextStepsSelect={false}>
        {qs.map((q, i) => (
          <Stepper.Step key={q.id} label={q.header || `Question ${i + 1}`} description={q.multiSelect ? 'several' : undefined}>
            <Stack gap="xs" mt="md">
              <Text fw={600}>{q.question}</Text>
              {q.multiSelect ? (
                <Checkbox.Group value={picked[q.id] ?? []} onChange={(v) => setPicked({ ...picked, [q.id]: v })}>
                  <Stack gap="xs">
                    {q.options.map((o) => <OptionCard key={o.label} option={o} multi />)}
                    {q.allowOther ? <OptionCard option={{ label: OTHER }} multi /> : null}
                  </Stack>
                </Checkbox.Group>
              ) : (
                <Radio.Group value={picked[q.id] ?? null} onChange={(v) => setPicked({ ...picked, [q.id]: v })}>
                  <Stack gap="xs">
                    {q.options.map((o) => <OptionCard key={o.label} option={o} />)}
                    {q.allowOther ? <OptionCard option={{ label: OTHER }} /> : null}
                  </Stack>
                </Radio.Group>
              )}
              {q.allowOther && (q.multiSelect ? (picked[q.id] ?? []).includes(OTHER) : picked[q.id] === OTHER) ? (
                <Textarea autosize placeholder="Your answer" value={other[q.id] ?? ''} onChange={(e) => setOther({ ...other, [q.id]: e.currentTarget.value })} />
              ) : null}
            </Stack>
          </Stepper.Step>
        ))}
        <Stepper.Completed>
          <Stack gap={6} mt="md">
            {qs.map((q) => (
              <Group key={q.id} gap="xs">
                <Text size="sm" c="dimmed" w={120}>{q.header || q.id}</Text>
                <Text size="sm" fw={500}>{[].concat(answerOf(q)).join(', ')}</Text>
              </Group>
            ))}
          </Stack>
        </Stepper.Completed>
      </Stepper>
      <Group justify="flex-end" mt="md">
        {step > 0 ? <Button variant="default" onClick={() => setStep(step - 1)}>Back</Button> : null}
        {step < qs.length ? (
          <Button onClick={() => setStep(step + 1)} disabled={!complete(qs[step])}>{step === qs.length - 1 ? 'Review' : 'Next'}</Button>
        ) : (
          <Button onClick={send}>Send answers</Button>
        )}
      </Group>
    </Frame>
  );
}

function OptionCard({ option, multi }) {
  const Card = multi ? Checkbox.Card : Radio.Card;
  const Indicator = multi ? Checkbox.Indicator : Radio.Indicator;
  const isOther = option.label === OTHER;
  return (
    <Card value={option.label} radius="md" p="sm" withBorder>
      <Group wrap="nowrap" align="flex-start">
        <Indicator />
        <div>
          <Text size="sm" fw={500}>{isOther ? 'Other…' : option.label}</Text>
          {option.description ? <Text size="xs" c="dimmed">{option.description}</Text> : null}
        </div>
      </Group>
    </Card>
  );
}

function Answered({ request }) {
  const v = request.answer;
  const text = request.inputType === 'questions'
    ? (request.questions ?? []).map((q) => `${q.header || q.id}: ${[].concat(v?.[q.id] ?? []).join(', ')}`).join(' · ')
    : String(v);
  return (
    <Paper radius="md" p="sm" bg="gray.0">
      <Text size="xs" c="dimmed">You answered</Text>
      <Text size="sm">{text}</Text>
      {request.error ? <Text size="xs" c="red">{request.error}</Text> : null}
    </Paper>
  );
}
