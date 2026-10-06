// This module holds the setup wizard's text fields. Form edits them with readline-style keys,
// and inputBox draws one in a box with its cursor.

import {
  box,
  style,
  pad,
  truncate,
  truncateLeft,
  tailCells,
  headCells,
  printable,
  width,
  cutStyled,
} from './render.js';

// A Field is one question in a form. back is how many characters sit after the cursor. about names
// the setting it covers, so a connection error can put the cursor back on the right field.
export interface Field {
  question?: string;
  help?: string;
  value?: string;
  placeholder?: string;
  secret?: boolean;
  optional?: boolean;
  back?: number;
  name?: string;
  about?: string;
  check?: (v: string) => string;
}

// KeyEvent is a decoded key press. A paste arrives as one event with key 'text'.
export interface KeyEvent {
  key: string;
  text?: string;
  alt?: boolean;
}

// wordStart and wordEnd find the edges of a word for moving and deleting by word. Spaces next to
// the cursor are skipped first.
function wordStart(r: string[], i: number): number {
  while (i > 0 && r[i - 1] === ' ') i--;
  while (i > 0 && r[i - 1] !== ' ') i--;
  return i;
}

function wordEnd(r: string[], i: number): number {
  while (i < r.length && r[i] === ' ') i++;
  while (i < r.length && r[i] !== ' ') i++;
  return i;
}

// Returns why a field's value can't be used yet, or '' when it's fine.
export function problem(f: Field): string {
  const v = (f.value ?? '').trim();
  if (!v) return f.optional ? '' : 'Type the ' + (f.name ?? '') + ' to continue.';
  return f.check?.(v) ?? '';
}

// A Form holds fields that are typed into one at a time. focus is the field being edited, and
// reveal shows secret values in plain text.
export class Form {
  focus = 0;
  reveal = false;

  constructor(public fields: Field[] = []) {}

  values(): string[] {
    return this.fields.map(f => (f.value ?? '').trim());
  }

  // Applies one key to the focused field. Returns true when the key submits it.
  key(event: string | KeyEvent): boolean {
    const e = typeof event === 'string' ? { key: event } : event;
    const k = e.key;
    if (!this.fields.length) return k === 'enter';

    // Field objects can be shared, like the bucket question in providers.ts, so work on copies.
    this.fields = this.fields.map(f => ({ ...f }));
    const f = this.fields[this.focus];
    const chars = [...(f.value ?? '')];
    const at = chars.length - Math.min(Math.max(f.back ?? 0, 0), chars.length);
    const before = chars.slice(0, at);
    const after = chars.slice(at);
    const set = (a: string[], b: string[]) => {
      f.value = a.join('') + b.join('');
      f.back = b.length;
    };

    // Alt keys move or delete by word, and alt+enter submits. Other alt keys do nothing.
    if (e.alt || k.startsWith('alt+')) {
      switch (k.replace(/^alt\+/, '')) {
        case 'left':
        case 'b':
          f.back = chars.length - wordStart(chars, at);
          break;
        case 'right':
        case 'f':
          f.back = chars.length - wordEnd(chars, at);
          break;
        case 'backspace':
          set(before.slice(0, wordStart(chars, at)), after);
          break;
        case 'enter':
          return true;
      }
      return false;
    }

    switch (k) {
      case 'enter':
        return true;
      case 'left':
      case 'ctrl+b':
        f.back = Math.min((f.back ?? 0) + 1, chars.length);
        break;
      case 'right':
      case 'ctrl+f':
        f.back = Math.max((f.back ?? 0) - 1, 0);
        break;
      case 'home':
      case 'ctrl+a':
        f.back = chars.length;
        break;
      case 'end':
      case 'ctrl+e':
        f.back = 0;
        break;
      case 'ctrl+left':
        f.back = chars.length - wordStart(chars, at);
        break;
      case 'ctrl+right':
        f.back = chars.length - wordEnd(chars, at);
        break;
      case 'backspace':
        if (before.length) set(before.slice(0, -1), after);
        break;
      case 'delete':
        if (after.length) set(before, after.slice(1));
        break;
      case 'ctrl+u':
        set([], after);
        break;
      case 'ctrl+k':
        set(before, []);
        break;
      case 'ctrl+w':
        set(before.slice(0, wordStart(chars, at)), after);
        break;
      case ' ':
        set([...before, ' '], after);
        break;
      default:
        // Anything else is typed or pasted text. Line breaks and tabs become spaces, and other control
        // characters are dropped.
        if (e.text !== undefined || [...k].length === 1)
          set([...before, ...(e.text ?? k).replace(/[\n\r\t]/g, ' ').replace(/[\u0000-\u001f]/g, '')], after);
    }
    return false;
  }
}

// Draws a field's value scrolled so the cursor stays in view. The cursor highlights the character
// under it, or shows as a block at the end. Secret values show as bullets unless revealed.
export function inputText(f: Field, reveal: boolean, focused: boolean, w: number): string {
  let chars = [...printable(f.value ?? '')];
  if (f.secret && !reveal) chars = chars.map(() => '•');
  const at = chars.length - Math.min(Math.max(f.back ?? 0, 0), chars.length);
  if (!focused) return style('text', truncateLeft(chars.join(''), w));

  const cursor = at < chars.length ? chars[at] : '█';
  const cw = width(cursor);
  const left = tailCells(chars.slice(0, at).join(''), Math.max(w - cw, 0));
  return cutStyled(
    style('text', left) +
      (at < chars.length
        ? style('selected', cursor) +
          style('text', headCells(chars.slice(at + 1).join(''), Math.max(w - width(left) - cw, 0)))
        : style('text', cursor)),
    Math.max(w, 0),
  );
}

// Draws a field in a box, showing its placeholder while it's empty.
export function inputBox(f: Field, reveal: boolean, focused: boolean, w: number): string {
  const inner = w - 4;
  let content: string;
  if (f.value) content = inputText(f, reveal, focused, inner);
  else if (focused) content = style('text', '█') + style('dim', truncate(f.placeholder ?? '', inner - 1));
  else content = style('dim', truncate(f.placeholder ?? '', inner));
  return box(pad(content, inner), focused, { w: w - 2 });
}
