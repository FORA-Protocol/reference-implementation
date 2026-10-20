// How a calendar is drawn: the pair of months on screen, what each dropdown
// may offer, the grid of cells a month fills, how each cell is marked, and
// which side of its button the panel opens on.
//
// It counts in UTC like period.js, and like that module it touches no
// document, so the tests exercise these rules directly. daterange.js is the
// part that turns them into elements.

import { DAY, MONTH_NAMES, addMonths, startOfMonth } from './period.js';

/** The weekday header row, starting on Sunday like the grids below. */
export const WEEKDAYS = ['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa'];

/**
 * calendarMonths is the pair of months the panel draws for a range: the month
 * the range starts in, and the month it ends in.
 *
 * Showing the month after the start instead would hide the far end of every
 * range longer than two months -- "last 6 months" drew March and April, so
 * only one of the two end markers was ever on screen. Pairing start with end
 * means both ends are always visible, at the cost of skipping the months in
 * between, which for a six-month range is the better trade.
 *
 * A range that begins and ends in one month would otherwise draw that month
 * twice, so the second calendar falls back to the month after it -- unless
 * that month is past today, in which case the pair steps back instead.
 *
 * @param {{from: number, to: number}} range
 * @param {{min: number, max: number}} [bounds]
 * @returns {{left: number, right: number}}
 */
export function calendarMonths(range, bounds) {
  const left = startOfMonth(range.from);
  const end = startOfMonth(range.to);
  const right = end > left ? end : addMonths(left, 1);
  if (bounds && right > bounds.max) {
    // A range inside the current month would otherwise put next month on the
    // right, which is past today. Show the month before it instead; both ends
    // of the range are still drawn, just both on the right-hand calendar.
    return { left: addMonths(bounds.max, -1), right: bounds.max };
  }
  return { left, right };
}

/**
 * YEARS_BACK is how many earlier years the year list offers.
 *
 * Nothing in the page says when a publisher's first sale was -- neither the
 * Exchange's totals nor the transaction list carries an earliest date -- so
 * there is no bound to derive and this is a chosen one. Five years of history
 * is far more than a dashboard whose longest readable period is a year needs.
 * Widen it here if a publisher ever needs to look further back; deriving it
 * would take a first-activity date from the Exchange.
 */
export const YEARS_BACK = 4;

/**
 * yearOptions is the list of years the year control offers, oldest first,
 * ending with the year now falls in. Which months of that last year are
 * offered is monthOptions' business, and it stops at the current one.
 * @param {number} today
 * @returns {number[]}
 */
export function yearOptions(today) {
  const last = new Date(today).getUTCFullYear();
  const years = [];
  for (let y = last - YEARS_BACK; y <= last; y++) {
    years.push(y);
  }
  return years;
}

/**
 * displayBounds is the first and last month a calendar may show, as
 * first-of-month timestamps.
 * @param {number} today
 * @returns {{min: number, max: number}}
 */
export function displayBounds(today) {
  return { min: Date.UTC(yearOptions(today)[0], 0, 1), max: startOfMonth(today) };
}

/**
 * monthOptions is the months the month control offers for a calendar sitting
 * in year: every month of an earlier year, and only the months up to the
 * current one in the year now falls in. Nothing later than today is offered.
 * @param {number} year
 * @param {number} today
 * @returns {Array<{name: string, index: number}>}
 */
export function monthOptions(year, today) {
  const now = new Date(today);
  const last = year === now.getUTCFullYear() ? now.getUTCMonth() : 11;
  return MONTH_NAMES.slice(0, last + 1).map((name, index) => ({ name, index }));
}

/**
 * moveCalendar answers what the two calendars show after one of them is sent
 * to month.
 *
 * Two rules hold whatever is asked for. Neither calendar leaves the window
 * bounds offers, and the left one always sits before the right one -- showing
 * the same month twice, or the later month on the left, would make the range
 * being drawn unreadable. The calendar that was not moved gives way; if it
 * cannot, because the moved one is against an edge of the window, the moved
 * one gives way instead, so a person is never stuck.
 *
 * @param {{left: number, right: number}} months
 * @param {'left' | 'right'} slot
 * @param {number} month
 * @param {{min: number, max: number}} bounds
 * @returns {{left: number, right: number}}
 */
export function moveCalendar(months, slot, month, bounds) {
  const clamp = (m) => Math.min(Math.max(m, bounds.min), bounds.max);
  let { left, right } = months;
  if (slot === 'left') {
    left = clamp(month);
    if (right <= left) {
      right = clamp(addMonths(left, 1));
      if (right <= left) {
        left = addMonths(right, -1);
      }
    }
    return { left, right };
  }
  right = clamp(month);
  if (left >= right) {
    left = clamp(addMonths(right, -1));
    if (left >= right) {
      right = addMonths(left, 1);
    }
  }
  return { left, right };
}

/**
 * dayModifiers says how one calendar cell is marked.
 *
 * A cell that belongs to a neighbouring month carries no selection marks at
 * all. Marking it by date alone drew the same calendar day twice: with two
 * months on screen the last days of the left one reappear as the leading cells
 * of the right one, so a single chosen day came out with two circles, and a
 * range's band was painted over days the calendar was only showing for
 * context. The neighbouring days stay visible, and stay clickable, they are
 * just not painted as chosen. A day later than today is drawn too, but it is
 * not selectable at all.
 *
 * @param {{ms: number, outside: boolean}} cell
 * @param {{from: number, to: number}} range
 * @param {number} today
 * @returns {string[]}
 */
export function dayModifiers(cell, range, today) {
  if (cell.outside) {
    return ['is-outside'];
  }
  if (cell.ms > today) {
    // Nothing is known about a day that has not happened, so it is drawn but
    // cannot be picked and never carries a selection mark.
    return ['is-future'];
  }
  const mods = [];
  if (cell.ms >= range.from && cell.ms <= range.to) {
    mods.push('is-in');
  }
  if (cell.ms === range.from || cell.ms === range.to) {
    mods.push('is-edge');
  }
  if (cell.ms === today) {
    mods.push('is-today');
  }
  return mods;
}

/**
 * monthTitle names a month as its heading reads.
 * @param {number} ms
 * @returns {string}
 */
export function monthTitle(ms) {
  const parts = monthTitleParts(ms);
  return `${parts.month} ${parts.year}`;
}

/**
 * monthTitleParts is a month's name and its year, which monthTitle joins into
 * the title a calendar is read by.
 * @param {number} ms
 * @returns {{month: string, year: string}}
 */
export function monthTitleParts(ms) {
  const d = new Date(ms);
  return { month: MONTH_NAMES[d.getUTCMonth()], year: String(d.getUTCFullYear()) };
}

/**
 * monthCells is the six-week grid a month is drawn on: every cell carries its
 * own day, and outside says whether it belongs to a neighbouring month. The
 * grid always starts on a Sunday and always holds 42 cells, so the calendar
 * does not change height from one month to the next.
 * @param {number} ms any day of the month to draw
 * @returns {Array<{ms: number, day: number, outside: boolean}>}
 */
export function monthCells(ms) {
  const first = startOfMonth(ms);
  const month = new Date(first).getUTCMonth();
  const start = first - new Date(first).getUTCDay() * DAY;
  const cells = [];
  for (let i = 0; i < 42; i++) {
    const at = start + i * DAY;
    const d = new Date(at);
    cells.push({ ms: at, day: d.getUTCDate(), outside: d.getUTCMonth() !== month });
  }
  return cells;
}

/**
 * panelSide says which edge of its button the picker's panel opens from.
 *
 * It opens from the button's end edge and runs leftwards, which suits a button
 * at the end of its row, as on most pages. On a page whose filters push the
 * button towards the start of the row, that would run the panel off the left
 * of the screen, so it opens from the start edge instead and runs rightwards.
 * When neither side fits, the one that hides less of the panel wins.
 * @param {{left: number, right: number}} button the button's box in the viewport
 * @param {number} width the panel's width
 * @param {number} viewport the viewport's width
 * @returns {'start' | 'end'}
 */
export function panelSide(button, width, viewport) {
  const endOverflow = Math.max(0, width - button.right);
  if (endOverflow === 0) {
    return 'end';
  }
  const startOverflow = Math.max(0, button.left + width - viewport);
  return startOverflow < endOverflow ? 'start' : 'end';
}

/**
 * selection is the range being picked, the preset it came from, and the two
 * months drawn beside it. It knows nothing about the page: draw gets its state
 * after every change.
 * @param {{from: number, to: number}} initial
 * @param {number} today
 * @param {(s: {range: object, anchor: number | null, chosen: string | null, months: object}) => void} draw
 */
export function selection(initial, today, draw) {
  let range = initial;
  let anchor = null; // the first day of a range being picked, before its second
  let chosen = null; // the preset last picked, while the range is still its own
  // Which two months are drawn. Chosen when a whole range arrives, and then
  // left alone while days are being clicked: recomputing mid-pick would move
  // the calendar out from under the person, because the range collapses to a
  // single day between the first click and the second.
  const bounds = displayBounds(today);
  let months = calendarMonths(range, bounds);
  const redraw = () => draw({ range, anchor, chosen, months });
  return {
    range: () => range,
    redraw,
    set(next, preset = null) {
      range = next;
      chosen = preset;
      anchor = null;
      months = calendarMonths(next, bounds);
      redraw();
    },
    pick(ms) {
      chosen = null;
      if (anchor === null) {
        anchor = ms;
        range = { from: ms, to: ms };
      } else {
        range = anchor <= ms ? { from: anchor, to: ms } : { from: ms, to: anchor };
        anchor = null;
      }
      redraw();
    },
    move(slot, month) {
      months = moveCalendar(months, slot, month, bounds);
      redraw();
    },
  };
}
