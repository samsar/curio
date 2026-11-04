/* The pages' theme. Without a choice, app.css follows the system
   (prefers-color-scheme); data-theme on <html> forces light or dark. This
   loads in <head>, before the stylesheet and without defer, so a choice
   kept from an earlier visit is applied before the first paint. The
   header's button (#theme-toggle, .js-only) switches to the theme not in
   effect: a choice that differs from the system's is kept in this
   browser's localStorage, and one that matches it is forgotten, so the
   page follows the system again. Storage may be refused (a private
   window, blocked site data): every access is guarded, and the button
   still switches for the page's lifetime. The button's icon is CSS's;
   its name, which says what it will do, is set here. It sends nothing. */
(function () {
  'use strict';

  const root = document.documentElement;
  const key = 'curio-theme';
  const system = matchMedia('(prefers-color-scheme: dark)');
  const systemTheme = () => (system.matches ? 'dark' : 'light');

  function stored() {
    try {
      const v = localStorage.getItem(key);
      return v === 'light' || v === 'dark' ? v : null;
    } catch (e) {
      return null;
    }
  }

  function store(theme) {
    try {
      if (theme === null) {
        localStorage.removeItem(key);
      } else {
        localStorage.setItem(key, theme);
      }
    } catch (e) {
      // no storage: the choice lasts as long as the page
    }
  }

  // apply forces theme, or follows the system for null or the system's own.
  function apply(theme) {
    if (theme === null || theme === systemTheme()) {
      root.removeAttribute('data-theme');
    } else {
      root.setAttribute('data-theme', theme);
    }
  }

  const current = () => root.getAttribute('data-theme') || systemTheme();
  function label() {
    const button = document.getElementById('theme-toggle');
    if (button) {
      const name = current() === 'dark' ? 'Switch to light theme' : 'Switch to dark theme';
      button.setAttribute('aria-label', name);
      button.setAttribute('title', name);
    }
  }

  // A kept choice the system now matches is forgotten: at load, and on change.
  function settle() {
    if (stored() === systemTheme()) {
      store(null);
    }
    apply(stored());
  }
  settle();

  document.addEventListener('DOMContentLoaded', label);
  document.addEventListener('click', function (event) {
    if (!(event.target instanceof Element) || !event.target.closest('#theme-toggle')) {
      return;
    }
    const next = current() === 'dark' ? 'light' : 'dark';
    apply(next);
    store(next === systemTheme() ? null : next);
    label();
  });
  system.addEventListener('change', () => { settle(); label(); });
})();
