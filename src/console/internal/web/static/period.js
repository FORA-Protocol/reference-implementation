// The date arithmetic the period picker is drawn from: presets, month grids,
// and the check of a range against the rules the server enforces, which the
// page hands over. It touches no document, so the tests exercise it directly;
// daterange.js is the part that draws it.
//
// Everything here counts in UTC, because the server does. Period.From and
// Period.To are midnight UTC and the Exchange buckets a totals read by UTC
// day, so a picker that worked in the viewer's own zone would hand back a
// different day than the one the person clicked whenever they sit east or
// west of Greenwich.

/** One day, in milliseconds. Every date here is a UTC midnight timestamp. */
export const DAY = 86400000;

/**
 * The rules a range is checked against before it is submitted, as the server
 * writes them into the picker's data-daterange-rules attribute: the longest
 * period it reads, and the sentences it refuses a reversed or too long one with.
 * @typedef {{max_days: number, reversed: string, too_long: string}} Rules
 */

export const MONTH_NAMES = [
  'January',
  'February',
  'March',
  'April',
  'May',
  'June',
  'July',
  'August',
  'September',
  'October',
  'November',
  'December',
];

/**
 * toISO renders a UTC midnight timestamp the way the server's date fields and
 * its query read it.
 * @param {number} ms
 * @returns {string}
 */
export function toISO(ms) {
  return new Date(ms).toISOString().slice(0, 10);
}

/**
 * fromISO reads a YYYY-MM-DD date as UTC midnight, or null when the text is
 * not one. The explicit part check refuses what Date.parse would otherwise
 * accept, such as "2026-13-40" rolling over into the next year.
 * @param {unknown} text
 * @returns {number | null}
 */
export function fromISO(text) {
  if (typeof text !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(text)) {
    return null;
  }
  const [y, m, d] = text.split('-').map(Number);
  const ms = Date.UTC(y, m - 1, d);
  const back = new Date(ms);
  if (back.getUTCFullYear() !== y || back.getUTCMonth() !== m - 1 || back.getUTCDate() !== d) {
    return null;
  }
  return ms;
}

/**
 * todayUTC is the UTC midnight of the day now falls in.
 * @param {Date} [now]
 * @returns {number}
 */
export function todayUTC(now = new Date()) {
  return Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
}

/**
 * startOfMonth is the first day of the month ms falls in.
 * @param {number} ms
 * @returns {number}
 */
export function startOfMonth(ms) {
  const d = new Date(ms);
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1);
}

/**
 * addMonths moves ms by n whole months, keeping it on a first-of-month.
 * @param {number} ms
 * @param {number} n
 * @returns {number}
 */
export function addMonths(ms, n) {
  const d = new Date(startOfMonth(ms));
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + n, 1);
}

/**
 * The presets the rail offers, in its order. calendar marks the ones named
 * after a calendar month or year rather than a count of days back.
 *
 * There is no "all time": the server refuses a period longer than a year, so
 * that entry could only ever produce an error. No preset runs past today,
 * because the days after it have not happened and the calendar will not pick
 * them: "This month" and "This year" run to today, and "This month" is also
 * what the page shows on a first visit, where the server defaults to the month
 * so far. The four that count backwards in months all end where the current
 * month begins, so "last month" and "last 3 months" read the same way rather
 * than one of them quietly including the month in progress.
 */
export const PRESETS = [
  { id: 'last7', label: 'Last 7 days', calendar: false },
  { id: 'last30', label: 'Last 30 days', calendar: false },
  { id: 'month', label: 'This month', calendar: true },
  { id: 'lastMonth', label: 'Last month', calendar: true },
  { id: 'last3Months', label: 'Last 3 months', calendar: true },
  { id: 'last6Months', label: 'Last 6 months', calendar: true },
  { id: 'year', label: 'This year', calendar: true },
];

/**
 * monthsBefore is the n whole calendar months that came before the one ms
 * falls in, ending on the last day of the previous month.
 *
 * "Last month" is one of this family rather than a case of its own, so the
 * four entries that count backwards in months cannot drift apart: each ends
 * where the current month begins, and none of them includes the month in
 * progress. Six whole months is at most 184 days, well inside what the server
 * accepts.
 *
 * @param {number} ms
 * @param {number} n
 * @returns {{from: number, to: number}}
 */
export function monthsBefore(ms, n) {
  const thisMonth = startOfMonth(ms);
  return { from: addMonths(thisMonth, -n), to: thisMonth - DAY };
}

/**
 * presetRange is the range a preset names, as inclusive UTC midnights.
 * @param {string} id
 * @param {number} today
 * @returns {{from: number, to: number} | null}
 */
export function presetRange(id, today) {
  switch (id) {
    case 'last7':
      return { from: today - 6 * DAY, to: today };
    case 'last30':
      return { from: today - 29 * DAY, to: today };
    case 'month':
      // Month to date, not the whole month: the rest of it has not happened.
      return { from: startOfMonth(today), to: today };
    case 'lastMonth':
      return monthsBefore(today, 1);
    case 'last3Months':
      return monthsBefore(today, 3);
    case 'last6Months':
      return monthsBefore(today, 6);
    case 'year':
      // Year to date, for the same reason as the month.
      return { from: Date.UTC(new Date(today).getUTCFullYear(), 0, 1), to: today };
    default:
      return null;
  }
}

/**
 * MATCH_ORDER is the order matchPreset tries the presets in when none was
 * chosen: the calendar ones before the rolling ones, each group in rail order.
 * Two presets can name the same days -- on the 30th of a 30-day month, "Last
 * 30 days" and "This month" are both the whole month, and on the 7th "Last 7
 * days" is the month so far -- and the calendar name is the one a publisher
 * reads a report by. It is derived from PRESETS, so a preset added there is
 * matched without a second edit here.
 */
const MATCH_ORDER = [
  ...PRESETS.filter((p) => p.calendar),
  ...PRESETS.filter((p) => !p.calendar),
].map((p) => p.id);

/**
 * matchPreset names the preset a range is exactly, or null when it is none of
 * them. It is what lights one entry in the rail. chosen is the preset the
 * publisher picked, which is named whenever it still is the range: otherwise
 * picking one of two presets that name the same days would light the other.
 * @param {{from: number, to: number}} range
 * @param {number} today
 * @param {string | null} [chosen]
 * @returns {string | null}
 */
export function matchPreset(range, today, chosen = null) {
  const is = (id) => {
    const p = presetRange(id, today);
    return p !== null && p.from === range.from && p.to === range.to;
  };
  if (chosen !== null && is(chosen)) {
    return chosen;
  }
  return MATCH_ORDER.find(is) ?? null;
}

/**
 * rangeDays counts the days a range covers, both ends included.
 * @param {{from: number, to: number}} range
 * @returns {number}
 */
export function rangeDays(range) {
  return Math.round((range.to - range.from) / DAY) + 1;
}

/**
 * rangeProblem says what is wrong with a range in the server's own words, or
 * empty when it is fine, so the picker cannot offer a range the server then
 * refuses.
 * @param {{from: number, to: number}} range
 * @param {Rules} rules
 * @returns {string}
 */
export function rangeProblem(range, rules) {
  if (range.to < range.from) {
    return rules.reversed;
  }
  if (rangeDays(range) > rules.max_days) {
    return rules.too_long;
  }
  return '';
}

/**
 * describeRange is the range in the words the footer shows.
 * @param {{from: number, to: number}} range
 * @returns {string}
 */
export function describeRange(range) {
  const day = (ms) => {
    const d = new Date(ms);
    return `${d.getUTCDate()} ${MONTH_NAMES[d.getUTCMonth()].slice(0, 3)} ${d.getUTCFullYear()}`;
  };
  if (range.from === range.to) {
    return `${day(range.from)} (UTC)`;
  }
  return `${day(range.from)} – ${day(range.to)} (UTC)`;
}
