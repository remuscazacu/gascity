import { useMemo, useState, type HTMLAttributes, type ReactNode } from 'react';

// Tables are typeset lists, not bordered grids. Column headers in
// Label scale (uppercase, tracked), rows separated by a hairline
// divider, no bordered cells. Tabular figures on (apply at the cell
// level for numeric columns).

export interface TableColumn<T> {
  key: string;
  label: string;
  sortable?: boolean;
  sortValue?: (row: T) => string | number | null | undefined;
  render: (row: T) => ReactNode;
  className?: string;
  align?: 'left' | 'right';
  /**
   * Right padding of the column's cells. 'normal' (pr-6) is the default;
   * 'tight' (pr-3) and 'none' (pr-0) let a narrow fixed-width column (a
   * checkbox, a short relative time) be exactly as wide as its content.
   */
  padRight?: 'normal' | 'tight' | 'none';
  /**
   * Extra classes for this column's body cells when the table stacks
   * (`stackBelowSm`). Write them with the `max-sm:` variant so they only
   * apply while the row is a grid, e.g. `max-sm:col-span-2`.
   */
  stackClassName?: string;
}

const PAD_RIGHT_CLASS: Record<NonNullable<TableColumn<unknown>['padRight']>, string> = {
  normal: 'pr-6',
  tight: 'pr-3',
  none: 'pr-0',
};

type SortDir = 'asc' | 'desc';
export interface SortState {
  key: string;
  dir: SortDir;
}

interface TableProps<T> {
  columns: ReadonlyArray<TableColumn<T>>;
  rows: ReadonlyArray<T>;
  rowKey: (row: T) => string;
  onRowClick?: (row: T) => void;
  rowProps?: (row: T) => HTMLAttributes<HTMLTableRowElement>;
  empty?: ReactNode;
  initialSort?: SortState | null;
  /**
   * 'auto' (default) sizes columns to their content. 'fixed' keeps the
   * width each column's className sets and gives the remainder to the
   * columns without one, so `truncate` content actually truncates instead
   * of widening the table past its box.
   */
  layout?: 'auto' | 'fixed';
  /**
   * Below the `sm` breakpoint, drop the header row and lay each row out
   * as a CSS grid (`rowClassName`, written with `max-sm:` variants) so a
   * phone never scrolls sideways. Columns place themselves with
   * `stackClassName`. The header's sort buttons are hidden with it, so
   * rows keep `initialSort` on a phone.
   */
  stackBelowSm?: { rowClassName: string };
}

export function Table<T>({
  columns,
  rows,
  rowKey,
  onRowClick,
  rowProps,
  empty,
  initialSort,
  layout = 'auto',
  stackBelowSm,
}: TableProps<T>) {
  const [sort, setSort] = useState<SortState | null>(initialSort ?? null);

  const sortedRows = useMemo(() => {
    if (sort === null) return rows;
    const col = columns.find((c) => c.key === sort.key);
    if (!col || !col.sortable) return rows;
    const getVal = col.sortValue ?? ((r: T) => String(col.render(r) ?? ''));
    const dir = sort.dir === 'asc' ? 1 : -1;
    return [...rows].sort((a, b) => {
      const av = getVal(a);
      const bv = getVal(b);
      if (av === bv) return 0;
      if (av === null || av === undefined) return -dir;
      if (bv === null || bv === undefined) return dir;
      if (av < bv) return -dir;
      if (av > bv) return dir;
      return 0;
    });
  }, [rows, columns, sort]);

  const toggleSort = (key: string) => {
    setSort((cur) => {
      if (cur?.key !== key) return { key, dir: 'asc' };
      return { key, dir: cur.dir === 'asc' ? 'desc' : 'asc' };
    });
  };

  return (
    <div className="overflow-x-auto">
      <table
        className={`w-full text-body tnum ${layout === 'fixed' ? 'table-fixed' : ''} ${
          stackBelowSm ? 'max-sm:block' : ''
        }`}
      >
        <thead className={stackBelowSm ? 'max-sm:hidden' : undefined}>
          <tr className="border-b border-rule text-label uppercase tracking-wider text-fg-muted">
            {columns.map((col) => {
              const isSorted = sort?.key === col.key;
              const align = col.align === 'right' ? 'text-right' : 'text-left';
              return (
                <th
                  key={col.key}
                  scope="col"
                  className={`pb-3 ${PAD_RIGHT_CLASS[col.padRight ?? 'normal']} font-medium select-none ${align} ${col.className ?? ''}`}
                >
                  {col.sortable ? (
                    <button
                      type="button"
                      onClick={() => toggleSort(col.key)}
                      className="inline-flex items-center gap-1 hover:text-fg transition-colors duration-150 ease-out-quart focus-mark rounded-sm"
                    >
                      {col.label}
                      {isSorted && (
                        <span aria-hidden className="text-accent">
                          {sort?.dir === 'asc' ? '↑' : '↓'}
                        </span>
                      )}
                    </button>
                  ) : (
                    col.label
                  )}
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody className={stackBelowSm ? 'max-sm:block' : undefined}>
          {sortedRows.length === 0 ? (
            <tr>
              <td colSpan={columns.length} className="py-10 text-center text-fg-muted italic">
                {empty ?? 'No data'}
              </td>
            </tr>
          ) : (
            sortedRows.map((row) => {
              const { className: rowClassName = '', ...extraRowProps } = rowProps?.(row) ?? {};
              return (
                <tr
                  {...extraRowProps}
                  key={rowKey(row)}
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                  className={`border-b border-rule transition-colors duration-150 ease-out-quart ${
                    onRowClick ? 'cursor-pointer hover:bg-surface-tint' : ''
                  } ${stackBelowSm?.rowClassName ?? ''} ${rowClassName}`}
                >
                  {columns.map((col) => {
                    const align = col.align === 'right' ? 'text-right' : 'text-left';
                    return (
                      <td
                        key={col.key}
                        className={`py-3 ${PAD_RIGHT_CLASS[col.padRight ?? 'normal']} align-baseline ${align} ${col.className ?? ''} ${
                          stackBelowSm ? `max-sm:p-0 ${col.stackClassName ?? ''}` : ''
                        }`}
                      >
                        {col.render(row)}
                      </td>
                    );
                  })}
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
}
