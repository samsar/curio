/* The dashboard's changes, and the upkeep of its live regions.

   A control carries its change as data attributes built in Go
   (internal/ui/actions.go): data-method, data-path under /v1/, and a
   button's data-body, a checkbox's state under data-field, or a form's
   fields, those sharing a name joined by data-join. This sends it as JSON,
   one change at a time and never twice, and writes what came of it into
   the .action-status data-status names: data-done (data-done-off for an
   unchecked checkbox), or the daemon's problem. The api package's
   TestDashboard_ActionsMatchTheAPI builds bodies by the same rules. The
   hooks at the end hold the pollers ([data-poll]) while nobody looks or a
   change is in flight, mark the page stale while the daemon doesn't
   answer, and leave an unchanged region alone. */
(function () {
  'use strict';

  const root = document.documentElement;
  root.setAttribute('data-js', '');

  const timeoutMs = 15000;
  const changed = 'curio:changed';
  let inFlight = false;

  function isCheckbox(control) {
    return control instanceof HTMLInputElement && control.type === 'checkbox';
  }

  function report(control, state, text) {
    const status = document.getElementById(control.getAttribute('data-status') || '');
    if (status) {
      status.setAttribute('data-state', state);
      status.textContent = text;
    }
  }

  // target is the URL data-path names: null unless it is a path under /v1/
  // of this page's own origin.
  function target(control) {
    let url;
    try {
      url = new URL(control.getAttribute('data-path') || '', location.origin);
    } catch (e) {
      return null; // not a URL at all: refused like any other
    }
    return url.origin === location.origin && url.pathname.startsWith('/v1/') ? url : null;
  }

  // body is the JSON a control sends, or null for none.
  function body(control) {
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

  // refusal is what the daemon said when it refused a change: its problem's
  // detail, else its title, else the status of an answer that isn't one.
  async function refusal(resp) {
    const p = await resp.json().catch(function () { return null; });
    const text = p && (p.detail || p.title);
    return typeof text === 'string' && text ? text : 'HTTP ' + resp.status;
  }

  // send sends control's change and reports what came of it: true when the
  // daemon took it.
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

  // setBack undoes a checkbox's change the daemon didn't take.
  function setBack(control, taken) {
    if (!taken && isCheckbox(control)) {
      control.checked = !control.checked;
    }
  }

  // act sends control's change, unless it is stored content's, disabled,
  // or another change is in flight.
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

  // A poll waits while nobody sees the page, or while a change is in
  // flight: its answer could land before the change's.
  document.addEventListener('htmx:beforeRequest', function (event) {
    if (fromPoller(event) && (document.hidden || inFlight)) {
      event.preventDefault();
    }
  });
  // An answer that isn't the page (the starting page, an error) marks it
  // stale and swaps nothing. Checked before the swap: a poller that lists
  // itself is replaced by it, and a replaced element's events no longer
  // reach the document.
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
  // A region that didn't change keeps its element, its focus, and what a
  // screen reader said of it.
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
})();
