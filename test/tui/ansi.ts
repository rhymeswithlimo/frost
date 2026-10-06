// Parses ANSI frames into grids of styled cells, so tests can compare rendered output column by column.
// The TUI parity and behaviour tests share these helpers.

import { cellWidth, graphemes } from '../../src/tui/render.js';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { convertProfile } from '../../src/cli/terminal.js';

// One terminal column. A wide grapheme fills its first cell and leaves empty text in the cells it covers.
export interface Cell {
  text: string;
  foreground?: string;
  background?: string;
  bold: boolean;
  underline: boolean;
}

// Splits a frame into rows of cells and tracks the SGR style in force at each one.
// Colours read as 'ansi:N' for 16 colours, 'index:N' for 256 colours and 'r,g,b' for truecolor.
export function cells(frame: string): Cell[][] {
  const rows: Cell[][] = [[]];
  let foreground: string | undefined,
    background: string | undefined,
    bold = false,
    underline = false;

  for (const token of frame.match(/\x1b\[[0-?]*[ -/]*[@-~]|[^\x1b]+/g) ?? []) {
    if (token.startsWith('\x1b')) {
      // Only SGR sequences change the style. Cursor moves and clears don't affect cells.
      if (!token.endsWith('m')) continue;
      const params = token.slice(2, -1).split(';').map(Number);
      for (let i = 0; i < params.length; i++) {
        const code = params[i];
        if (code === 0) {
          foreground = background = undefined;
          bold = underline = false;
        } else if (code === 1) bold = true;
        else if (code === 22) bold = false;
        else if (code === 4) underline = true;
        else if (code === 24) underline = false;
        else if (code === 39) foreground = undefined;
        else if (code === 49) background = undefined;
        else if ((code >= 30 && code <= 37) || (code >= 90 && code <= 97)) foreground = 'ansi:' + code;
        // Backgrounds record the matching foreground code, so one colour map covers both.
        else if ((code >= 40 && code <= 47) || (code >= 100 && code <= 107)) background = 'ansi:' + (code - 10);
        else if ((code === 38 || code === 48) && params[i + 1] === 5) {
          const color = 'index:' + params[i + 2];
          if (code === 38) foreground = color;
          else background = color;
          i += 2;
        } else if ((code === 38 || code === 48) && params[i + 1] === 2) {
          const color = params.slice(i + 2, i + 5).join(',');
          if (code === 38) foreground = color;
          else background = color;
          i += 4;
        } else throw new Error('unsupported reference color ' + token);
      }
      continue;
    }

    for (const text of graphemes(token)) {
      if (text === '\n') {
        rows.push([]);
        continue;
      }
      // Zero-width graphemes add no cells. Wide ones add a cell per column, with the text in the first.
      const cell = { text, foreground, background, bold, underline };
      for (let i = 0; i < cellWidth(text); i++) rows.at(-1)!.push({ ...cell, text: i ? '' : text });
    }
  }
  return rows;
}

// Like cells, but blank cells only show their background, so their foreground, bold and underline are dropped.
export function visualCells(frame: string): Cell[][] {
  return cells(frame).map(row =>
    row.map(c => (/^[\s]*$/.test(c.text) ? { ...c, foreground: undefined, bold: false, underline: false } : c)),
  );
}

// Maps each palette hex to its SGR parameters in every colour profile. Tests run from dist/test/tui, so the
// fixtures are three folders up.
const referenceColors = JSON.parse(
  readFileSync(new URL('../../../test/fixtures/cli/colors.json', import.meta.url), 'utf8'),
) as Record<string, Record<string, string>>;

// Converts a truecolor frame to the 16 and 256 colour profiles and compares each with the reference, recoloured
// through colors.json. The plain-text profile must match the reference with every style removed.
export function assertProfiles(actual: string, reference: string, name: string): void {
  const original = visualCells(reference);
  for (const profile of ['ansi', 'ansi256'] as const) {
    // Translate the reference's 'r,g,b' cells into the colour names this profile should produce.
    const map = new Map<string, string>();
    for (const [hex, sequence] of Object.entries(referenceColors.truecolor))
      map.set(
        sequence.replace('38;2;', '').replaceAll(';', ','),
        profile === 'ansi'
          ? 'ansi:' + referenceColors[profile][hex]
          : 'index:' + referenceColors[profile][hex].replace('38;5;', ''),
      );
    const expected = original.map(row =>
      row.map(cell => ({
        ...cell,
        foreground: cell.foreground ? map.get(cell.foreground) : undefined,
        background: cell.background ? map.get(cell.background) : undefined,
      })),
    );

    const converted = visualCells(convertProfile(actual, profile));
    assert.equal(converted.length, expected.length, name + ' ' + profile);
    for (let row = 0; row < expected.length; row++)
      assert.deepEqual(converted[row], expected[row], name + ' ' + profile + ' row ' + (row + 1));
  }

  assert.equal(convertProfile(actual, 'ascii'), reference.replace(/\x1b\[[\d;]*m/g, ''), name + ' ascii');
}
