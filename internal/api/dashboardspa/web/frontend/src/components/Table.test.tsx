import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { Table, type TableColumn } from './Table';

// sr-u9ua: Table's fixed layout, phone stacking and per-column padding are
// opt-in. These tests pin that the default render is unchanged for the routes
// that don't ask for them, and that the opt-ins land on the right elements.

interface Row {
  id: string;
  name: string;
}

const rows: Row[] = [{ id: 'a', name: 'alpha' }];

const columns: TableColumn<Row>[] = [
  { key: 'check', label: '', className: 'w-6', padRight: 'none', render: () => 'x' },
  { key: 'name', label: 'Name', render: (r) => r.name, stackClassName: 'max-sm:col-span-2' },
];

afterEach(() => cleanup());

describe('Table layout opt-ins (sr-u9ua)', () => {
  it('keeps the auto layout and no stacking classes by default', () => {
    const { container } = render(
      <Table
        columns={[{ key: 'name', label: 'Name', render: (r: Row) => r.name }]}
        rows={rows}
        rowKey={(r) => r.id}
      />,
    );
    const table = container.querySelector('table')!;
    expect(table.className).not.toContain('table-fixed');
    expect(container.innerHTML).not.toContain('max-sm:');
    expect(container.querySelector('td')!.className).toContain('pr-6');
  });

  it("adds table-fixed for layout='fixed'", () => {
    const { container } = render(
      <Table columns={columns} rows={rows} rowKey={(r) => r.id} layout="fixed" />,
    );
    expect(container.querySelector('table')!.className).toContain('table-fixed');
  });

  it('applies padRight to both the header and body cells of that column', () => {
    const { container } = render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    const [th] = Array.from(container.querySelectorAll('th'));
    const [td] = Array.from(container.querySelectorAll('td'));
    expect(th!.className).toContain('pr-0');
    expect(th!.className).not.toContain('pr-6');
    expect(td!.className).toContain('pr-0');
    expect(td!.className).not.toContain('pr-6');
  });

  it('only applies stackClassName when stackBelowSm is set', () => {
    const plain = render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    expect(plain.container.innerHTML).not.toContain('max-sm:col-span-2');
    plain.unmount();

    const { container } = render(
      <Table
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        stackBelowSm={{ rowClassName: 'max-sm:grid' }}
      />,
    );
    expect(container.querySelector('thead')!.className).toContain('max-sm:hidden');
    expect(container.querySelector('tbody tr')!.className).toContain('max-sm:grid');
    const cells = Array.from(container.querySelectorAll('tbody td'));
    expect(cells[1]!.className).toContain('max-sm:col-span-2');
    expect(cells.every((c) => c.className.includes('max-sm:p-0'))).toBe(true);
  });
});
