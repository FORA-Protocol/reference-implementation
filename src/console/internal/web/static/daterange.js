// The period picker: two months side by side, a rail of presets, and one
// range that the form's hidden from/to fields carry to the server.
//
// The widget owns no state the server does not already hold. It reads the two
// fields when it opens and writes them back when the range is applied, then
// submits their form, so the server reads the same from/to it always did.
//
// The date arithmetic lives in period.js, which counts in UTC because the
// server does.

import {
  WEEKDAYS,
  dayModifiers,
  monthCells,
  monthOptions,
  panelSide,
  selection,
  yearOptions,
} from './calendar.js';
import {
  PRESETS,
  describeRange,
  fromISO,
  matchPreset,
  presetRange,
  rangeProblem,
  toISO,
  todayUTC,
} from './period.js';

/** The heading's two controls, named once so markup and selector agree. */
const SLOT_MONTH = 'data-daterange-slot-month';
const SLOT_YEAR = 'data-daterange-slot-year';

/**
 * mount wires one picker to the two hidden fields that carry its period. It
 * opens on the period they hold, and writes the applied range back into them
 * and submits their form.
 * @param {HTMLElement} root the [data-daterange] element
 * @param {HTMLInputElement} fromField
 * @param {HTMLInputElement} toField
 */
function mount(root, fromField, toField) {
  const panel = root.querySelector('[data-daterange-panel]');
  const trigger = root.querySelector('[data-daterange-trigger]');
  const rules = JSON.parse(root.dataset.daterangeRules);
  const today = todayUTC();
  const read = () => ({
    from: fromISO(fromField.value) ?? today,
    to: fromISO(toField.value) ?? today,
  });
  const picked = selection(read(), today, (s) => render(root, { ...s, today, rules }));

  const state = {
    set: picked.set,
    pick: picked.pick,
    move: picked.move,
    show(open) {
      panel.hidden = !open;
      trigger.setAttribute('aria-expanded', String(open));
      if (open) {
        picked.set(read());
        place(panel, trigger);
      }
    },
    apply() {
      const range = picked.range();
      if (rangeProblem(range, rules)) {
        return;
      }
      fromField.value = toISO(range.from);
      toField.value = toISO(range.to);
      fromField.form?.requestSubmit();
    },
    get open() {
      return !panel.hidden;
    },
  };

  wire(root, state);
  picked.redraw();
}

/**
 * place opens the panel on the side of its button that keeps it on screen. It
 * runs once the panel is drawn and visible, because the panel's width is its
 * content's and a hidden element has none.
 * @param {HTMLElement} panel
 * @param {HTMLElement} trigger
 */
function place(panel, trigger) {
  const side = panelSide(
    trigger.getBoundingClientRect(),
    panel.offsetWidth,
    document.documentElement.clientWidth,
  );
  panel.classList.toggle('daterange-panel-start', side === 'start');
}

/**
 * render redraws the rail, both months and the footer from the current state.
 * @param {HTMLElement} root
 * @param {{range: object, anchor: number | null, chosen: string | null, months: object, today: number, rules: object}} s
 */
function render(root, s) {
  const q = (name) => root.querySelector(`[data-daterange-${name}]`);
  const active = matchPreset(s.range, s.today, s.chosen);
  q('presets').replaceChildren(...PRESETS.map((p) => presetItem(p, p.id === active)));
  for (const [i, slot] of ['left', 'right'].entries()) {
    drawMonth(root.querySelector(`[data-daterange-month="${i}"]`), s.months[slot], slot, s);
  }
  const problem = rangeProblem(s.range, s.rules);
  q('note').textContent = problem;
  q('summary').textContent = problem ? '—' : describeRange(s.range);
  q('apply').disabled = problem !== '';
}

/**
 * presetItem is one entry of the rail.
 * @param {{id: string, label: string}} preset
 * @param {boolean} active
 * @returns {HTMLLIElement}
 */
function presetItem(preset, active) {
  const li = document.createElement('li');
  const b = document.createElement('button');
  b.type = 'button';
  b.className = `daterange-preset${active ? ' is-active' : ''}`;
  b.dataset.daterangePreset = preset.id;
  b.textContent = preset.label;
  if (active) {
    b.setAttribute('aria-current', 'true');
  }
  li.append(b);
  return li;
}

/**
 * drawMonth fills one month: its heading, the weekday row and 42 day cells.
 * @param {HTMLElement | null} host
 * @param {number} month
 * @param {{range: object, anchor: number | null, today: number}} s
 */
function drawMonth(host, month, slot, s) {
  if (!host) {
    return;
  }
  const head = monthHeading(month, slot, s.today);
  const grid = document.createElement('div');
  grid.className = 'daterange-grid';
  for (const name of WEEKDAYS) {
    const el = document.createElement('span');
    el.className = 'daterange-weekday';
    el.textContent = name;
    grid.append(el);
  }
  for (const cell of monthCells(month)) {
    grid.append(dayButton(cell, s));
  }
  host.replaceChildren(head, grid);
}

/**
 * monthHeading is the calendar's title, drawn as two dropdowns, the month and
 * the year. Changing either moves this calendar to the month they now name: it
 * changes what is shown, not the range being picked.
 * @param {number} month the first day of the month the calendar shows
 * @param {'left' | 'right'} slot which of the two calendars it heads
 * @param {number} today
 * @returns {HTMLParagraphElement}
 */
function monthHeading(month, slot, today) {
  const at = new Date(month);
  const year = at.getUTCFullYear();
  const head = document.createElement('p');
  head.className = 'daterange-title';
  head.append(
    chooser(
      SLOT_MONTH,
      slot,
      'Month',
      monthOptions(year, today).map((m) => [m.index, m.name]),
      at.getUTCMonth(),
    ),
    chooser(
      SLOT_YEAR,
      slot,
      'Year',
      yearOptions(today).map((y) => [y, String(y)]),
      year,
    ),
  );
  return head;
}

/**
 * chooser is one dropdown in a calendar's heading.
 *
 * The list it offers is the whole cap. A month or a year past today is not an
 * option a person can pick, so nothing has to be refused after the fact and
 * there is no state where the control shows one thing and the calendar another.
 *
 * @param {string} attr the hyphenated hook the change handler matches
 * @param {'left' | 'right'} slot which calendar it drives
 * @param {string} label what a screen reader announces
 * @param {Array<[number, string]>} items value and its text, in order
 * @param {number} current the value to show as chosen
 * @returns {HTMLSelectElement}
 */
function chooser(attr, slot, label, items, current) {
  const select = document.createElement('select');
  select.className = 'daterange-choose';
  select.setAttribute(attr, slot);
  select.setAttribute('aria-label', label);
  for (const [value, text] of items) {
    const option = document.createElement('option');
    option.value = String(value);
    option.textContent = text;
    option.selected = value === current;
    select.append(option);
  }
  return select;
}

/**
 * onPanelChange sends a calendar to the month its two dropdowns now name.
 *
 * Both are read together rather than acting on the one that changed, because
 * a month and a year only mean something as a pair: choosing December while
 * the year control still says the current one names a month that has not
 * happened. moveCalendar is what then pulls it back inside the cap.
 *
 * @param {Event} event
 * @param {{move: Function}} state
 */
function onPanelChange(event, state) {
  const select = event.target.closest(`[${SLOT_MONTH}],[${SLOT_YEAR}]`);
  if (!select) {
    return;
  }
  const slot = select.getAttribute(SLOT_MONTH) ?? select.getAttribute(SLOT_YEAR);
  const heading = select.closest('.daterange-title');
  const month = Number(heading.querySelector(`[${SLOT_MONTH}]`).value);
  const year = Number(heading.querySelector(`[${SLOT_YEAR}]`).value);
  state.move(slot, Date.UTC(year, month, 1));
}

/**
 * dayButton is one day cell, marked for its place in the chosen range.
 * @param {{ms: number, day: number, outside: boolean}} cell
 * @param {{range: object, anchor: number | null, today: number}} s
 * @returns {HTMLButtonElement}
 */
function dayButton(cell, s) {
  const mods = dayModifiers(cell, s.range, s.today);
  const b = document.createElement('button');
  b.type = 'button';
  b.dataset.daterangeDay = String(cell.ms);
  b.textContent = String(cell.day);
  b.className = ['daterange-day', ...mods].join(' ');
  b.setAttribute('aria-label', new Date(cell.ms).toISOString().slice(0, 10));
  b.disabled = mods.includes('is-future');
  if (mods.includes('is-edge')) {
    b.setAttribute('aria-current', 'date');
  }
  return b;
}

/**
 * wire attaches the panel's events. Clicks are read off the panel itself
 * rather than off each cell, so a redraw never leaves a listener behind on a
 * button that no longer exists.
 * @param {HTMLElement} root
 * @param {object} state
 */
function wire(root, state) {
  const panel = root.querySelector('[data-daterange-panel]');
  root
    .querySelector('[data-daterange-trigger]')
    .addEventListener('click', () => state.show(!state.open));
  root.querySelector('[data-daterange-cancel]').addEventListener('click', () => state.show(false));
  root.querySelector('[data-daterange-apply]').addEventListener('click', () => state.apply());
  panel.addEventListener('click', (event) => onPanelClick(event, state));
  panel.addEventListener('change', (event) => onPanelChange(event, state));
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && state.open) {
      state.show(false);
    }
  });
  // Capture, not bubble, and this is the whole reason: clicking a day redraws
  // the calendar, which detaches the very button that was clicked. By the time
  // a bubbling listener on the document ran, event.target was no longer inside
  // root -- it was no longer inside the page at all -- so every click on a day,
  // a preset or an arrow read as a click outside and shut the panel. The
  // capture phase runs before the panel's own handler, while the target is
  // still in the tree.
  document.addEventListener(
    'click',
    (event) => {
      if (state.open && !root.contains(event.target)) {
        state.show(false);
      }
    },
    true,
  );
}

/**
 * onPanelClick routes a click inside the panel to the one thing it means.
 * @param {MouseEvent} event
 * @param {object} state
 */
function onPanelClick(event, state) {
  const el = event.target.closest('[data-daterange-day],[data-daterange-preset]');
  if (!el) {
    return;
  }
  const d = el.dataset;
  if (d.daterangeDay) {
    state.pick(Number(d.daterangeDay));
    return;
  }
  const next = presetRange(d.daterangePreset, todayUTC());
  if (next) {
    state.set(next, d.daterangePreset);
  }
}

/**
 * init wires every period picker on the page to its form's from/to fields. It
 * runs only in a browser, so importing this module elsewhere touches no
 * document.
 */
function init() {
  for (const root of document.querySelectorAll('[data-daterange]')) {
    const form = root.closest('form');
    // Named form controls rather than a selector: the form's own index is
    // what the server reads when it submits, so asking it by name cannot
    // drift from the field that actually travels.
    const fromField = form?.elements.namedItem('from');
    const toField = form?.elements.namedItem('to');
    if (!fromField || !toField) {
      continue; // No fields to write the period into: nothing to drive.
    }
    mount(root, fromField, toField);
  }
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
}
