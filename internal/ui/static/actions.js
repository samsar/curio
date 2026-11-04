/* The dashboard's changes, the upkeep of its live regions, and its /.
   A control carries its change as data attributes built in Go
   (internal/ui/actions.go): data-method, data-path under /v1/, and a
   button's data-body, a checkbox's state under data-field, or a form's
   fields, those sharing a name joined by data-join. This sends it as JSON,
   one change at a time, and writes what came of it into the .action-status
   data-status names: data-done (data-done-off for an unchecked checkbox) or
   the daemon's problem (TestDashboard_ActionsMatchTheAPI mirrors it). Its
   hooks hold the pollers ([data-poll]) while nobody looks or a change is in
   flight, mark the page stale while the daemon doesn't answer, and leave an
   unchanged region alone. / focuses the page's search box. */
(function () {
  'use strict';

  const root = document.documentElement;
  root.setAttribute('data-js', '');

  const timeoutMs = 15000;
  const changed = 'curio:changed';
  let inFlight = false;
  const isCheckbox = control => control instanceof HTMLInputElement && control.type === 'checkbox';

  function report(control, state, text) {
    const status = document.getElementById(control.getAttribute('data-status') || '');
    if (status) {
      status.setAttribute('data-state', state);
      status.textContent = text;
    }
  }

  // target is the URL data-path names: null but for a path under /v1/ here.
  function target(control) {
    let url;
    try {
      url = new URL(control.getAttribute('data-path') || '', location.origin);
    } catch (e) {
      return null; // not a URL at all: refused like any other
    }
    return url.origin === location.origin && url.pathname.startsWith('/v1/') ? url : null;
  }

  function body(control) { // the JSON a control sends, or null for none
    if (isCheckbox(control)) {
      return JSON.stringify({[control.getAttribute('data-field')]: control.checked});
    }
    if (!(control instanceof HTMLFormElement)) {
      return control.getAttribute('data-body');
    }
    const values = {};
    new FormData(control).forEach(function (value, name) {
      (values[name] = values[name] || []).push(value);
    });
    const fields = {};
    for (const name of Object.keys(values)) {
      fields[name] = values[name].join(control.getAttribute('data-join') || '');
    }
    return JSON.stringify(fields);
  }

  // refusal is a refused change's problem's detail, title, or else status.
  async function refusal(resp) {
    const p = await resp.json().catch(function () { return null; });
    const text = p && (p.detail || p.title);
    return typeof text === 'string' && text ? text : 'HTTP ' + resp.status;
  }

  // send sends control's change and reports it: true when the daemon took it.
  async function send(control) {
    const method = control.getAttribute('data-method');
    const url = target(control);
    if ((method !== 'POST' && method !== 'PUT') || !url) {
      report(control, 'error', 'This control names no change the daemon takes; nothing was sent.');
      return false;
    }
    const init = {method: method, signal: AbortSignal.timeout(timeoutMs)};
    const payload = body(control);
    if (payload !== null) {
      init.headers = {'Content-Type': 'application/json'};
      init.body = payload;
    }
    let resp;
    try {
      resp = await fetch(url, init);
    } catch (e) {
      report(control, 'error', 'curio-daemon didn\'t answer. Is it running? (curio status)');
      return false;
    }
    if (!resp.ok) {
      report(control, 'error', await refusal(resp));
      return false;
    }
    const off = isCheckbox(control) && !control.checked;
    report(control, 'ok', control.getAttribute(off ? 'data-done-off' : 'data-done') || '');
    return true;
  }

  function setBack(control, taken) { // undoes a checkbox's change the daemon didn't take
    if (!taken && isCheckbox(control)) {
      control.checked = !control.checked;
    }
  }

  // act sends control's change: not stored content's, a disabled one's or a second.
  async function act(control) {
    if (control.closest('.prose') || control.disabled || control.getAttribute('aria-disabled') === 'true' ||
        inFlight) {
      setBack(control, false);
      return;
    }
    inFlight = true;
    control.setAttribute('aria-busy', 'true');
    report(control, 'busy', 'Working…');
    let taken = false;
    try {
      taken = await send(control);
    } catch (e) { // before fetch: a browser without what send uses, say
      report(control, 'error', 'The change wasn\'t sent (' + e + ').');
    } finally {
      setBack(control, taken);
      control.removeAttribute('aria-busy');
      inFlight = false; // before the event, or its refresh would be held
      document.body.dispatchEvent(new CustomEvent(changed));
    }
  }

  function delegate(type, selector) {
    document.addEventListener(type, function (event) {
      const control = event.target.closest(selector);
      if (control) {
        act(control);
      }
    });
  }
  delegate('click', 'button[data-method]');
  delegate('change', 'input[type="checkbox"][data-method]');
  document.addEventListener('submit', function (event) {
    const form = event.target.closest('form[data-method]');
    if (form) {
      event.preventDefault();
      if (form.reportValidity()) {
        act(form);
      }
    }
  });

  function fromPoller(event) {
    const elt = event.detail && event.detail.elt;
    return elt instanceof Element && elt.hasAttribute('data-poll');
  }

  // A poll waits while nobody looks, or for a change in flight, whose answer it could beat.
  document.addEventListener('htmx:beforeRequest', function (event) {
    if (fromPoller(event) && (document.hidden || inFlight)) {
      event.preventDefault();
    }
  });
  // An answer that isn't the page (starting, an error) marks it stale and swaps
  // nothing, checked before the swap, which replaces a poller listing itself.
  document.addEventListener('htmx:beforeSwap', function (event) {
    if (!fromPoller(event)) {
      return;
    }
    const failed = event.detail.isError === true;
    root.toggleAttribute('data-stale', failed);
    if (failed || inFlight) {
      event.detail.shouldSwap = false;
    }
  });
  document.addEventListener('htmx:sendError', function (event) {
    if (fromPoller(event)) {
      root.setAttribute('data-stale', '');
    }
  });
  // A region that didn't change keeps its element, focus and what was said.
  document.addEventListener('htmx:oobBeforeSwap', function (event) {
    const incoming = event.detail.fragment && event.detail.fragment.firstElementChild;
    if (incoming && incoming.isEqualNode(event.detail.target)) {
      event.detail.shouldSwap = false;
    }
  });
  document.addEventListener('visibilitychange', function () {
    if (!document.hidden) {
      document.body.dispatchEvent(new CustomEvent(changed));
    }
  });
  // / focuses the search box: not in a field, with a modifier, over a confirm, or once a page took it.
  window.addEventListener('keydown', function (event) {
    if (event.key !== '/' || event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey ||
        document.querySelector(':popover-open') ||
        (event.target instanceof Element && event.target.closest('input, textarea, select, [contenteditable]'))) {
      return;
    }
    const box = document.querySelector('input[type="search"][name="q"]');
    if (box && box.offsetParent !== null) {
      event.preventDefault();
      box.focus();
      box.select();
    }
  });
})();
