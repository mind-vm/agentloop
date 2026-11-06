// The render blocks ext.BlocksPack emits, drawn with Mantine.
//
// Adapted from process-mining's console (studio-apps), whose payload shapes
// the pack keeps. The pack already refuses specs that would draw empty, so
// this side stays defensive only against what Go cannot see; a block it does
// not recognise is shown as its JSON rather than dropped — a dropped block is
// a number the agent computed and the reader never saw.
import { Badge, Card, Code, Group, SimpleGrid, Table, Text } from '@mantine/core';
import { AreaChart, BarChart, DonutChart, LineChart } from '@mantine/charts';

const str = (p, k) => (typeof p[k] === 'string' ? p[k] : '');
const list = (p, k) => (Array.isArray(p[k]) ? p[k] : []);

// One palette for every chart, so the same series keeps its colour when two
// charts in one answer show it.
const COLORS = ['blue.6', 'teal.6', 'grape.6', 'orange.6', 'cyan.6', 'pink.6'];

export function BlockView({ block }) {
  switch (block.type) {
    case 'markdown':
      return <MarkdownBlock payload={block.payload} />;
    case 'table':
      return <TableBlock payload={block.payload} />;
    case 'chart':
      return <ChartBlock payload={block.payload} />;
    case 'stats':
      return <StatsBlock payload={block.payload} />;
    default:
      return <UnknownBlock block={block} />;
  }
}

function Framed({ title, children }) {
  return (
    <Card withBorder padding="md" radius="md">
      {title ? <Text fw={600} size="sm" mb="xs">{title}</Text> : null}
      {children}
    </Card>
  );
}

// Text with its line breaks kept, not a markdown parser: the agent writes a
// paragraph here, and a parser is a dependency and an injection surface for it.
function MarkdownBlock({ payload }) {
  return <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>{str(payload, 'markdown')}</Text>;
}

const cell = (v) => (v == null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v));

function TableBlock({ payload }) {
  const columns = list(payload, 'columns');
  const rows = list(payload, 'rows');
  return (
    <Framed title={str(payload, 'title')}>
      <Table.ScrollContainer minWidth={400}>
        <Table striped withTableBorder highlightOnHover>
          <Table.Thead>
            <Table.Tr>{columns.map((c) => <Table.Th key={c}>{c}</Table.Th>)}</Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {rows.map((r, i) => (
              <Table.Tr key={i}>{columns.map((c) => <Table.Td key={c}>{cell(r[c])}</Table.Td>)}</Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      {payload.truncated ? (
        <Text size="xs" c="dimmed" mt={6}>Showing {rows.length} of {payload.totalRows} rows.</Text>
      ) : null}
    </Framed>
  );
}

function ChartBlock({ payload }) {
  const variant = str(payload, 'variant') || 'bar';
  const data = list(payload, 'data');
  const x = str(payload, 'x');
  const series = list(payload, 'series').map((name, i) => ({ name, color: COLORS[i % COLORS.length] }));
  const common = { h: 260, data, dataKey: x, series };
  let chart;
  if (variant === 'line') chart = <LineChart {...common} curveType="linear" />;
  else if (variant === 'area') chart = <AreaChart {...common} />;
  else if (variant === 'donut' || variant === 'pie')
    chart = (
      <DonutChart
        h={260}
        withLabelsLine
        withLabels
        data={data.map((r, i) => ({ name: cell(r[x]), value: Number(r[series[0]?.name] ?? 0), color: COLORS[i % COLORS.length] }))}
      />
    );
  // Every other variant the pack declares is drawn as bars rather than
  // dropped: a chart in the wrong shape still carries its numbers.
  else chart = <BarChart {...common} />;
  return <Framed title={str(payload, 'title')}>{chart}</Framed>;
}

function StatsBlock({ payload }) {
  const items = list(payload, 'items');
  const cols = typeof payload.cols === 'number' ? payload.cols : Math.min(items.length || 1, 4);
  return (
    <Framed title={str(payload, 'title')}>
      <SimpleGrid cols={{ base: 1, sm: Math.max(1, cols) }}>
        {items.map((it, i) => (
          <Card key={i} withBorder padding="sm" radius="sm">
            <Text size="xs" c="dimmed">{str(it, 'label')}</Text>
            <Group gap={6} align="baseline">
              <Text fw={700} size="lg">{str(it, 'value')}</Text>
              {str(it, 'unit') ? <Text size="xs" c="dimmed">{str(it, 'unit')}</Text> : null}
              {typeof it.delta === 'number' ? (
                <Badge size="xs" color={it.delta >= 0 ? 'teal' : 'red'} variant="light">
                  {it.delta >= 0 ? '+' : ''}{Math.round(it.delta * 100)}%
                </Badge>
              ) : null}
            </Group>
            {str(it, 'hint') ? <Text size="xs" c="dimmed">{str(it, 'hint')}</Text> : null}
          </Card>
        ))}
      </SimpleGrid>
    </Framed>
  );
}

function UnknownBlock({ block }) {
  return (
    <Framed title={`Unrecognised block: ${block.type}`}>
      <Code block>{JSON.stringify(block.payload, null, 2)}</Code>
    </Framed>
  );
}
