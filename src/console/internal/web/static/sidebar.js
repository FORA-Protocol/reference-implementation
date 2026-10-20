// The sidebar's collapse button, and remembering it between pages.
//
// This is a classic script loaded in the head, not deferred, so the saved
// state is applied before the page is drawn and a collapsed sidebar does not
// open and shut again on every navigation. Browser storage can be missing or
// refuse access in a private window; the sidebar then simply starts open.

const STORE = 'fora.sidebar';
const COLLAPSED = 'sidebar-collapsed';

try {
  if (localStorage.getItem(STORE) === 'collapsed') {
    document.documentElement.classList.add(COLLAPSED);
  }
} catch {
  // Nothing remembered: the sidebar starts open.
}

document.addEventListener('DOMContentLoaded', () => {
  const toggle = document.getElementById('sidebar-toggle');
  if (!toggle) {
    return;
  }
  const show = (collapsed) => {
    toggle.setAttribute('aria-expanded', String(!collapsed));
    toggle.title = collapsed ? 'Expand the menu' : 'Collapse the menu';
  };
  show(document.documentElement.classList.contains(COLLAPSED));
  toggle.addEventListener('click', () => {
    const collapsed = document.documentElement.classList.toggle(COLLAPSED);
    show(collapsed);
    try {
      localStorage.setItem(STORE, collapsed ? 'collapsed' : 'open');
    } catch {
      // Not remembered; the next page starts open.
    }
  });
});
