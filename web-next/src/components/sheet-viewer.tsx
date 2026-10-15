// A spreadsheet or a CSV (board ViewerSheet): column letters and row
// numbers, a cell to select, a formula bar with the cell's reference and its
// formula or value, and the sheets as tabs under the grid. Read-only.
import { useState } from "react"
import type { KeyboardEvent } from "react"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { cn } from "@/lib/utils"
import { ViewerTools } from "./file-viewer-frame"
import "./sheet-viewer.css"

export type Sheet = {
  name: string
  rows: string[][]
  /** A cell's formula, without its "=", where it has one (xlsx only). */
  formulas?: Array<Array<string | undefined>>
  clipped: boolean
}

/** 0 → A, 25 → Z, 26 → AA: a column's letters, as a spreadsheet names it. */
export function columnName(index: number): string {
  let name = ""
  let rest = index + 1
  while (rest > 0) {
    const digit = (rest - 1) % 26
    name = String.fromCharCode(65 + digit) + name
    rest = Math.floor((rest - 1) / 26)
  }
  return name
}

type Cell = { row: number; column: number }

function Grid({ sheet }: { sheet: Sheet }) {
  const [cell, setCell] = useState<Cell>({ row: 0, column: 0 })
  const columns = sheet.rows.reduce(
    (most, row) => Math.max(most, row.length),
    0
  )
  const value = sheet.rows[cell.row]?.[cell.column] ?? ""
  const formula = sheet.formulas?.[cell.row]?.[cell.column]
  const reference = `${columnName(cell.column)}${cell.row + 1}`

  function move(event: KeyboardEvent<HTMLDivElement>) {
    const steps: Partial<Record<string, [number, number]>> = {
      ArrowUp: [-1, 0],
      ArrowDown: [1, 0],
      ArrowLeft: [0, -1],
      ArrowRight: [0, 1],
    }
    const step = steps[event.key]
    if (!step || !sheet.rows.length) return
    event.preventDefault()
    setCell((current) => ({
      row: Math.min(sheet.rows.length - 1, Math.max(0, current.row + step[0])),
      column: Math.min(
        Math.max(0, columns - 1),
        Math.max(0, current.column + step[1])
      ),
    }))
  }

  return (
    <>
      <div className="sheet-formula" aria-live="polite">
        <span className="sheet-formula-ref" aria-label="Cell">
          {reference}
        </span>
        <span className="sheet-formula-fx" aria-hidden="true">
          fx
        </span>
        <output className="sheet-formula-value" aria-label="Formula or value">
          {formula ? `=${formula}` : value}
        </output>
      </div>
      <div
        className="workspace-document-scroll sheet-grid"
        tabIndex={0}
        role="region"
        aria-label={`${sheet.name}, ${reference} selected. Arrow keys move.`}
        onKeyDown={move}
      >
        <table>
          <thead>
            <tr>
              <th className="sheet-corner" aria-hidden="true" />
              {Array.from({ length: columns }, (_, column) => (
                <th
                  key={column}
                  scope="col"
                  data-current={column === cell.column ? "true" : undefined}
                >
                  {columnName(column)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {sheet.rows.map((row, rowIndex) => (
              <tr key={rowIndex}>
                <th
                  scope="row"
                  data-current={rowIndex === cell.row ? "true" : undefined}
                >
                  {rowIndex + 1}
                </th>
                {Array.from({ length: columns }, (_, column) => {
                  const selected =
                    rowIndex === cell.row && column === cell.column
                  return (
                    <td
                      key={column}
                      className={cn(selected && "is-selected")}
                      aria-selected={selected || undefined}
                      onClick={() => setCell({ row: rowIndex, column })}
                    >
                      {row[column] ?? ""}
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
        {sheet.clipped && (
          <p className="sheet-clipped">
            Showing the first 300 rows and 40 columns.
          </p>
        )}
      </div>
    </>
  )
}

export function SheetViewer({ sheets }: { sheets: Sheet[] }) {
  const [selected, setSelected] = useState(0)
  const sheet = sheets[selected] ?? sheets[0]
  return (
    <div className="sheet-viewer">
      {sheets.length > 1 && (
        <ViewerTools>
          <span className="sheet-count">{sheets.length} sheets</span>
        </ViewerTools>
      )}
      <Grid key={selected} sheet={sheet} />
      {sheets.length > 1 && (
        <Tabs
          value={selected}
          onValueChange={(value) => setSelected(Number(value))}
          className="sheet-tabs"
        >
          <TabsList aria-label="Sheets">
            {sheets.map((item, index) => (
              <TabsTrigger key={`${index}-${item.name}`} value={index}>
                {item.name}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      )}
    </div>
  )
}
