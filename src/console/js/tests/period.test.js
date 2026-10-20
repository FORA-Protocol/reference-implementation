import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
  calendarMonths,
  dayModifiers,
  displayBounds,
  monthCells,
  monthOptions,
  monthTitle,
  monthTitleParts,
  moveCalendar,
  panelSide,
  selection,
  yearOptions,
} from '../../internal/web/static/calendar.js';
import {
  DAY,
  PRESETS,
  addMonths,
  describeRange,
  fromISO,
  matchPreset,
  monthsBefore,
  presetRange,
  rangeDays,
  rangeProblem,
  startOfMonth,
  toISO,
  todayUTC,
} from '../../internal/web/static/period.js';

// A day late in a month, so the month presets have days on both sides of it,
// and in a zone-sensitive hour: 23:30 UTC is already the next day east of
// Greenwich and still the previous one to the west. Anything that read the
// viewer's own clock instead of UTC lands on the wrong day here.
const NOW = new Date('2026-10-22T23:30:00Z');

// The rules the server hands the picker. The fixture is what a page carries;
// a console test keeps the two equal.
const RULES = JSON.parse(
  readFileSync(new URL('./testdata/period-rules.json', import.meta.url), 'utf8'),
);
const TODAY = Date.UTC(2026, 9, 22);

describe('toISO and fromISO', () => {
  it('round-trips a date', () => {
    expect(fromISO(toISO(TODAY))).toBe(TODAY);
  });

  it('reads the form the server writes', () => {
    expect(fromISO('2026-10-22')).toBe(TODAY);
    expect(toISO(TODAY)).toBe('2026-10-22');
  });

  it.each([
    ['not a date', 'yesterday'],
    ['a month past twelve', '2026-13-01'],
    ['a day past the month', '2026-02-30'],
    ['a two-digit year', '26-10-22'],
    ['an empty field', ''],
    ['a number', 20261022],
    ['nothing at all', undefined],
  ])('refuses %s', (_name, input) => {
    expect(fromISO(input)).toBeNull();
  });

  it('refuses a day that would roll into the next month', () => {
    // Date.UTC(2026, 10, 31) silently becomes 1 December. A picker that let
    // that through would show a day the server never agreed to.
    expect(fromISO('2026-11-31')).toBeNull();
  });
});

describe('todayUTC', () => {
  it('takes the UTC day, not the local one', () => {
    expect(todayUTC(NOW)).toBe(TODAY);
  });

  it('is midnight', () => {
    expect(todayUTC(NOW) % DAY).toBe(0);
  });
});

describe('month arithmetic', () => {
  it('finds the first of the month', () => {
    expect(toISO(startOfMonth(TODAY))).toBe('2026-10-01');
  });

  it('steps forward across a year end', () => {
    expect(toISO(addMonths(Date.UTC(2026, 11, 5), 1))).toBe('2027-01-01');
  });

  it('steps back across a year start', () => {
    expect(toISO(addMonths(Date.UTC(2026, 0, 5), -1))).toBe('2025-12-01');
  });

  it('names the month it draws', () => {
    expect(monthTitle(TODAY)).toBe('October 2026');
  });
});

describe('presetRange', () => {
  const on = (id) => {
    const r = presetRange(id, TODAY);
    return [toISO(r.from), toISO(r.to)];
  };

  it.each([
    ['last7', ['2026-10-16', '2026-10-22']],
    ['last30', ['2026-09-23', '2026-10-22']],
    ['month', ['2026-10-01', '2026-10-22']],
    ['lastMonth', ['2026-09-01', '2026-09-30']],
    ['last3Months', ['2026-07-01', '2026-09-30']],
    ['last6Months', ['2026-04-01', '2026-09-30']],
    ['year', ['2026-01-01', '2026-10-22']],
  ])('%s covers the right days', (id, want) => {
    expect(on(id)).toEqual(want);
  });

  it('has no entry for the days that were dropped from the rail', () => {
    // "Today" and "Yesterday" were removed: a publisher reads a period, and a
    // single day is not one. Nothing may answer to those names any more.
    expect(presetRange('today', TODAY)).toBeNull();
    expect(presetRange('yesterday', TODAY)).toBeNull();
    expect(PRESETS.map((p) => p.id)).not.toContain('today');
    expect(PRESETS.map((p) => p.id)).not.toContain('yesterday');
  });

  it('puts the two new entries straight after last month', () => {
    expect(PRESETS.map((p) => p.label)).toEqual([
      'Last 7 days',
      'Last 30 days',
      'This month',
      'Last month',
      'Last 3 months',
      'Last 6 months',
      'This year',
    ]);
  });

  it('never lets a months-back preset reach into the month in progress', () => {
    // Each one ends the day before the current month starts, so none of them
    // mixes a finished month with a partial one.
    for (const id of ['lastMonth', 'last3Months', 'last6Months']) {
      expect(toISO(presetRange(id, TODAY).to)).toBe('2026-09-30');
    }
  });

  it('counts whole months back across a year boundary', () => {
    const jan = Date.UTC(2027, 0, 15);
    expect(toISO(presetRange('last3Months', jan).from)).toBe('2026-10-01');
    expect(toISO(presetRange('last3Months', jan).to)).toBe('2026-12-31');
  });

  it('counts the last seven days including today', () => {
    expect(rangeDays(presetRange('last7', TODAY))).toBe(7);
  });

  it('counts the last thirty days including today', () => {
    expect(rangeDays(presetRange('last30', TODAY))).toBe(30);
  });

  it('has no range for a name it does not know', () => {
    expect(presetRange('all-time', TODAY)).toBeNull();
  });

  it('offers only ranges the server accepts', () => {
    // Every preset has to survive the same check the server applies, or the
    // rail would hold an entry that can only ever produce an error.
    for (const { id } of PRESETS) {
      expect(rangeProblem(presetRange(id, TODAY), RULES)).toBe('');
    }
  });

  it('keeps the longest year-to-date inside the limit', () => {
    // 31 December of a leap year is the most days a year-to-date can cover.
    const leap = presetRange('year', Date.UTC(2028, 11, 31));
    expect(rangeDays(leap)).toBe(366);
    expect(rangeProblem(leap, RULES)).toBe('');
  });
});

describe('matchPreset', () => {
  it('names the preset a range is', () => {
    expect(matchPreset(presetRange('last30', TODAY), TODAY)).toBe('last30');
  });

  it('names the month so far', () => {
    expect(matchPreset({ from: Date.UTC(2026, 9, 1), to: TODAY }, TODAY)).toBe('month');
  });

  it('does not name the whole current month, which runs past today', () => {
    // No preset covers days that have not happened. The server's default is
    // the month so far too, so a first visit lights "This month".
    expect(
      matchPreset({ from: Date.UTC(2026, 9, 1), to: Date.UTC(2026, 9, 31) }, TODAY),
    ).toBeNull();
  });

  it('prefers every calendar preset over every rolling one', () => {
    // On 7 January the year so far is also the last 7 days.
    const seventh = Date.UTC(2027, 0, 7);
    expect(matchPreset(presetRange('last7', seventh), seventh)).toBe('month');
  });

  it('names nothing for a range picked by hand', () => {
    expect(matchPreset({ from: Date.UTC(2026, 9, 3), to: Date.UTC(2026, 9, 9) }, TODAY)).toBeNull();
  });

  // On the 30th of a 30-day month the whole month is both "This month" and
  // "Last 30 days". Picking either must light the one picked.
  const lastOfSeptember = Date.UTC(2026, 8, 30);
  const september = { from: Date.UTC(2026, 8, 1), to: lastOfSeptember };

  it('names the preset that was picked when two name the same days', () => {
    expect(matchPreset(september, lastOfSeptember, 'month')).toBe('month');
    expect(matchPreset(september, lastOfSeptember, 'last30')).toBe('last30');
  });

  it('prefers the calendar name when none was picked', () => {
    expect(matchPreset(september, lastOfSeptember)).toBe('month');
  });

  it('ignores a picked preset the range is no longer', () => {
    expect(
      matchPreset({ from: Date.UTC(2026, 8, 3), to: lastOfSeptember }, lastOfSeptember, 'month'),
    ).toBeNull();
  });
});

describe('rangeProblem', () => {
  it('passes an ordinary range', () => {
    expect(rangeProblem({ from: TODAY - 5 * DAY, to: TODAY }, RULES)).toBe('');
  });

  it('passes a single day', () => {
    expect(rangeProblem({ from: TODAY, to: TODAY }, RULES)).toBe('');
  });

  it('refuses a range that ends before it starts', () => {
    expect(rangeProblem({ from: TODAY, to: TODAY - DAY }, RULES)).toBe(RULES.reversed);
  });

  it('allows the longest period the server takes', () => {
    const r = { from: TODAY - (RULES.max_days - 1) * DAY, to: TODAY };
    expect(rangeDays(r)).toBe(RULES.max_days);
    expect(rangeProblem(r, RULES)).toBe('');
  });

  it('refuses one day more than that', () => {
    expect(rangeProblem({ from: TODAY - RULES.max_days * DAY, to: TODAY }, RULES)).toBe(
      RULES.too_long,
    );
  });
});

describe('describeRange', () => {
  it('says both ends of a range', () => {
    expect(describeRange({ from: Date.UTC(2026, 9, 12), to: Date.UTC(2026, 10, 8) })).toBe(
      '12 Oct 2026 – 8 Nov 2026 (UTC)',
    );
  });

  it('says a single day once', () => {
    expect(describeRange({ from: TODAY, to: TODAY })).toBe('22 Oct 2026 (UTC)');
  });
});

describe('calendarMonths', () => {
  const shown = (range) => {
    const m = calendarMonths(range, displayBounds(TODAY));
    return [monthTitle(m.left), monthTitle(m.right)];
  };
  const on = (id) => shown(presetRange(id, TODAY));

  it('pairs the month a range starts in with the month it ends in', () => {
    // The whole point: for a range spanning several months, both ends have to
    // be on screen. Drawing the month after the start hid the far end.
    expect(on('last3Months')).toEqual(['July 2026', 'September 2026']);
    expect(on('last6Months')).toEqual(['April 2026', 'September 2026']);
    expect(on('year')).toEqual(['January 2026', 'October 2026']);
  });

  it('shows the month after the start when a range sits inside one month', () => {
    // November has not happened, so the pair steps back instead of forward.
    expect(on('last7')).toEqual(['September 2026', 'October 2026']);
    expect(on('month')).toEqual(['September 2026', 'October 2026']);
  });

  it('shows two adjacent months when a range straddles exactly two', () => {
    expect(on('last30')).toEqual(['September 2026', 'October 2026']);
    expect(on('lastMonth')).toEqual(['September 2026', 'October 2026']);
  });

  it('puts both end markers on screen for every preset', () => {
    // Each preset must draw its first and its last day, so a person can see
    // what they picked rather than only read it in the summary line.
    for (const { id } of PRESETS) {
      const range = presetRange(id, TODAY);
      const m = calendarMonths(range, displayBounds(TODAY));
      const cells = [...monthCells(m.left), ...monthCells(m.right)];
      const edges = cells.filter((c) => dayModifiers(c, range, TODAY).includes('is-edge'));
      expect(edges.map((c) => toISO(c.ms)).sort()).toEqual(
        [toISO(range.from), toISO(range.to)].sort(),
      );
    }
  });

  it('never draws the same month twice', () => {
    for (const { id } of PRESETS) {
      const m = calendarMonths(presetRange(id, TODAY), displayBounds(TODAY));
      expect(m.right).toBeGreaterThan(m.left);
    }
  });
});

describe('nothing later than today', () => {
  it('stops the month-to-date and year-to-date presets at today', () => {
    expect(toISO(presetRange('month', TODAY).to)).toBe('2026-10-22');
    expect(toISO(presetRange('year', TODAY).to)).toBe('2026-10-22');
    expect(toISO(presetRange('month', TODAY).from)).toBe('2026-10-01');
    expect(toISO(presetRange('year', TODAY).from)).toBe('2026-01-01');
  });

  it('never lets any preset run past today', () => {
    for (const { id } of PRESETS) {
      expect(presetRange(id, TODAY).to).toBeLessThanOrEqual(TODAY);
    }
  });

  it('offers years up to the current one and no further', () => {
    const years = yearOptions(TODAY);
    expect(years[years.length - 1]).toBe(2026);
    expect(years).toHaveLength(5);
  });

  it('offers only the months that have started, in the current year', () => {
    const names = monthOptions(2026, TODAY).map((m) => m.name);
    expect(names).toHaveLength(10);
    expect(names[names.length - 1]).toBe('October');
  });

  it('offers every month of an earlier year', () => {
    expect(monthOptions(2025, TODAY)).toHaveLength(12);
  });

  it('stops the calendars at the month now falls in', () => {
    expect(toISO(displayBounds(TODAY).max)).toBe('2026-10-01');
    expect(toISO(displayBounds(TODAY).min)).toBe('2022-01-01');
  });

  it('refuses to draw a month after today even for a range inside this one', () => {
    // Month to date starts and ends in October, so the second calendar would
    // normally be November. That has not happened, so the pair steps back.
    const bounds = displayBounds(TODAY);
    const m = calendarMonths(presetRange('month', TODAY), bounds);
    expect([monthTitle(m.left), monthTitle(m.right)]).toEqual(['September 2026', 'October 2026']);
  });

  it('marks a day after today as unpickable and never as chosen', () => {
    const range = presetRange('month', TODAY);
    expect(dayModifiers({ ms: Date.UTC(2026, 9, 23), outside: false }, range, TODAY)).toEqual([
      'is-future',
    ]);
    expect(dayModifiers({ ms: TODAY, outside: false }, range, TODAY)).toEqual([
      'is-in',
      'is-edge',
      'is-today',
    ]);
  });
});

describe('moveCalendar', () => {
  const bounds = displayBounds(TODAY);
  const pair = (l, r) => ({ left: Date.UTC(2026, l, 1), right: Date.UTC(2026, r, 1) });
  const names = (m) => [monthTitle(m.left), monthTitle(m.right)];

  it('sends the calendar the person moved where they asked', () => {
    expect(names(moveCalendar(pair(5, 7), 'left', Date.UTC(2026, 2, 1), bounds))).toEqual([
      'March 2026',
      'August 2026',
    ]);
  });

  it('pushes the other calendar out of the way rather than crossing it', () => {
    // Left sent past right: right gives way instead of the two swapping.
    expect(names(moveCalendar(pair(5, 7), 'left', Date.UTC(2026, 8, 1), bounds))).toEqual([
      'September 2026',
      'October 2026',
    ]);
    expect(names(moveCalendar(pair(5, 7), 'right', Date.UTC(2026, 2, 1), bounds))).toEqual([
      'February 2026',
      'March 2026',
    ]);
  });

  it('never shows the same month in both calendars', () => {
    const m = moveCalendar(pair(5, 7), 'left', Date.UTC(2026, 7, 1), bounds);
    expect(m.right).toBeGreaterThan(m.left);
  });

  it('keeps both calendars inside the cap', () => {
    const far = moveCalendar(pair(5, 7), 'right', Date.UTC(2030, 5, 1), bounds);
    expect(far.right).toBeLessThanOrEqual(bounds.max);
    expect(far.left).toBeGreaterThanOrEqual(bounds.min);
    const old = moveCalendar(pair(5, 7), 'left', Date.UTC(1999, 0, 1), bounds);
    expect(old.left).toBeGreaterThanOrEqual(bounds.min);
  });

  it('gives way itself when the calendar it moved is against an edge', () => {
    // Left sent to the last allowed month: the right one cannot go beyond it,
    // so the left steps back instead and the person is not stuck.
    const m = moveCalendar(pair(5, 7), 'left', bounds.max, bounds);
    expect(m.right).toBe(bounds.max);
    expect(m.left).toBeLessThan(m.right);
  });
});

describe('monthsBefore', () => {
  it('is what "last month" means, so the family cannot drift', () => {
    expect(monthsBefore(TODAY, 1)).toEqual(presetRange('lastMonth', TODAY));
  });

  it('takes whole months and stops before the current one', () => {
    const r = monthsBefore(TODAY, 6);
    expect([toISO(r.from), toISO(r.to)]).toEqual(['2026-04-01', '2026-09-30']);
  });

  it('stays inside what the server accepts', () => {
    expect(rangeProblem(monthsBefore(TODAY, 6), RULES)).toBe('');
    // The longest six whole months run March-August in a leap year: 184 days.
    expect(rangeDays(monthsBefore(Date.UTC(2028, 8, 10), 6))).toBe(184);
  });
});

describe('monthTitleParts', () => {
  it('splits the heading into the two controls it is drawn as', () => {
    expect(monthTitleParts(Date.UTC(2026, 9, 22))).toEqual({
      month: 'October',
      year: '2026',
    });
  });

  it('still reads as one line', () => {
    const p = monthTitleParts(TODAY);
    expect(`${p.month} ${p.year}`).toBe(monthTitle(TODAY));
  });
});

describe('dayModifiers', () => {
  const own = (ms) => ({ ms, outside: false });
  const neighbour = (ms) => ({ ms, outside: true });
  const sep = (d) => Date.UTC(2026, 8, d);
  // Every case below sits in September, which is wholly in the past relative
  // to TODAY. Passing TODAY as "today" keeps them clear of the future rule.

  it('marks the one chosen day as both ends of the range', () => {
    const one = { from: sep(29), to: sep(29) };
    expect(dayModifiers(own(sep(29)), one, sep(29)).sort()).toEqual([
      'is-edge',
      'is-in',
      'is-today',
    ]);
  });

  it('marks the days between the ends', () => {
    const week = { from: sep(23), to: sep(29) };
    expect(dayModifiers(own(sep(26)), week, TODAY)).toEqual(['is-in']);
  });

  it('marks each end', () => {
    const week = { from: sep(23), to: sep(29) };
    expect(dayModifiers(own(sep(23)), week, TODAY)).toEqual(['is-in', 'is-edge']);
    expect(dayModifiers(own(sep(29)), week, TODAY)).toEqual(['is-in', 'is-edge']);
  });

  it('leaves a day outside the range unmarked', () => {
    const week = { from: sep(23), to: sep(29) };
    expect(dayModifiers(own(sep(22)), week, TODAY)).toEqual([]);
  });

  // The defect this function exists for. Two months are on screen at once, so
  // the last days of the left one are drawn again as the leading cells of the
  // right one. Marking those by date alone gave a single chosen day two
  // circles, and painted a range's band across days shown only for context.
  it('never marks a day belonging to a neighbouring month', () => {
    const one = { from: sep(29), to: sep(29) };
    expect(dayModifiers(neighbour(sep(29)), one, sep(29))).toEqual(['is-outside']);
  });

  it('draws exactly one end marker per end across both calendars', () => {
    const month = { from: sep(1), to: sep(30) };
    const cells = [...monthCells(sep(15)), ...monthCells(Date.UTC(2026, 9, 15))];
    const edges = cells.filter((c) => dayModifiers(c, month, TODAY).includes('is-edge'));
    expect(edges.map((c) => toISO(c.ms))).toEqual(['2026-09-01', '2026-09-30']);
  });

  it('paints a range only on the days each calendar owns', () => {
    const week = { from: sep(23), to: sep(29) };
    const cells = [...monthCells(sep(15)), ...monthCells(Date.UTC(2026, 9, 15))];
    const painted = cells.filter((c) => dayModifiers(c, week, TODAY).includes('is-in'));
    expect(painted).toHaveLength(rangeDays(week));
  });

  it('paints a whole month once, not once per calendar', () => {
    const month = { from: sep(1), to: sep(30) };
    const cells = [...monthCells(sep(15)), ...monthCells(Date.UTC(2026, 9, 15))];
    const painted = cells.filter((c) => dayModifiers(c, month, TODAY).includes('is-in'));
    expect(painted).toHaveLength(30);
  });
});

describe('monthCells', () => {
  const cells = monthCells(TODAY);

  it('always draws six weeks, so the panel keeps its height', () => {
    expect(cells).toHaveLength(42);
    expect(monthCells(Date.UTC(2026, 1, 15))).toHaveLength(42);
  });

  it('starts on a Sunday', () => {
    expect(new Date(cells[0].ms).getUTCDay()).toBe(0);
  });

  it('runs one day at a time', () => {
    for (let i = 1; i < cells.length; i++) {
      expect(cells[i].ms - cells[i - 1].ms).toBe(DAY);
    }
  });

  it('marks the days that belong to a neighbouring month', () => {
    const own = cells.filter((c) => !c.outside);
    expect(own).toHaveLength(31);
    expect(toISO(own[0].ms)).toBe('2026-10-01');
    expect(toISO(own[own.length - 1].ms)).toBe('2026-10-31');
  });

  it('carries the day of the month each cell shows', () => {
    expect(cells.find((c) => toISO(c.ms) === '2026-10-22').day).toBe(22);
  });

  it('draws February of a leap year', () => {
    const feb = monthCells(Date.UTC(2028, 1, 10)).filter((c) => !c.outside);
    expect(feb).toHaveLength(29);
  });
});

describe('panelSide', () => {
  const width = 600;
  const viewport = 1400;

  it('opens leftwards from a button at the end of its row', () => {
    expect(panelSide({ left: 1100, right: 1300 }, width, viewport)).toBe('end');
  });

  it('opens rightwards when leftwards would run off the left of the screen', () => {
    expect(panelSide({ left: 240, right: 440 }, width, viewport)).toBe('start');
  });

  it('keeps opening leftwards when it fits exactly', () => {
    expect(panelSide({ left: 400, right: width }, width, viewport)).toBe('end');
  });

  it('takes the side that hides less when neither fits', () => {
    expect(panelSide({ left: 300, right: 500 }, width, 700)).toBe('end');
    expect(panelSide({ left: 50, right: 250 }, width, 700)).toBe('start');
  });
});

describe('selection', () => {
  // A selection that records every state it draws, so a test reads the last.
  const recorded = (initial) => {
    const drawn = [];
    const s = selection(initial, TODAY, (state) => drawn.push(state));
    return { s, last: () => drawn[drawn.length - 1] };
  };
  const october = { from: Date.UTC(2026, 9, 1), to: TODAY };

  it('picks a range in two clicks, in either order', () => {
    const { s, last } = recorded(october);
    s.pick(Date.UTC(2026, 9, 12));
    expect(last().anchor).toBe(Date.UTC(2026, 9, 12));
    expect(last().range).toEqual({ from: Date.UTC(2026, 9, 12), to: Date.UTC(2026, 9, 12) });
    s.pick(Date.UTC(2026, 9, 5));
    expect(last().anchor).toBeNull();
    expect(s.range()).toEqual({ from: Date.UTC(2026, 9, 5), to: Date.UTC(2026, 9, 12) });
  });

  it('keeps the months still while days are clicked, and moves them for a whole range', () => {
    const { s, last } = recorded(october);
    s.redraw();
    const before = last().months;
    s.pick(Date.UTC(2026, 3, 3));
    expect(last().months).toEqual(before);
    s.set(presetRange('last6Months', TODAY), 'last6Months');
    expect(last().months).not.toEqual(before);
    expect(last().chosen).toBe('last6Months');
  });

  it('forgets the chosen preset once a day is clicked', () => {
    const { s, last } = recorded(october);
    s.set(presetRange('last7', TODAY), 'last7');
    s.pick(Date.UTC(2026, 9, 2));
    expect(last().chosen).toBeNull();
  });
});
