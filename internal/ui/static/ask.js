/* Ask mode on the search page (search.html's "ask" says what its shell
   holds). The switch over the box sets the page's data-mode, which shows
   the search's results or the answer, and gives the box and its button
   the mode's words, from the switch's own attributes: a page opened in
   Ask mode has Search's until this script gives it Ask's, since without
   it Enter searches. In Ask mode, Enter or the button asks the box's
   words (POST /v1/ask) and the answer streams in as JSON lines, written
   here as text alone. Typing sends nothing: htmx's searches are dropped
   as it confirms them, and the form's submit never reaches htmx. No
   address asks as it loads, so the writer's model loads only on Enter.
   Entering Ask aborts the search in flight, the follow-up's rerank among
   them, which would land under the answer and take its address. One ask
   at a time: a new one, or leaving Ask, aborts the last, which ends its
   answer in the daemon; one left ends on a note saying so, what it showed
   kept for a return to Ask. Each ask keeps its question in the address by
   replaceState. Back in Search, the address #results had is put back,
   and the box's words are searched again, in a history entry of their
   own, unless they are the words #results shows.

   Every address comes from the shell's data attributes, every page's name
   from the daemon (ui.DocRef's), every note from Go (templates by key),
   and where each citation stands from the done event. No text of the
   daemon's or the browser's is shown: an ask refused, failed or cut off
   ends on Go's words for the kind, with the request's ID for the daemon's
   log. The script's own words are its chrome: what an ask is busy with,
   its headings, and its own bounds. A stream is bounded as curio ask
   bounds it, half a minute of silence (the daemon writes every 10 s) and
   a line of 1 MiB, and in all to 4 MiB, which the page holds. */
(function () {
  'use strict';

  const page = document.querySelector('.search-page');
  const shell = document.getElementById('ask');
  const box = document.getElementById('q');
  if (page === null || shell === null || box === null || box.form === null) {
    return;
  }
  const form = box.form;
  const submit = form.querySelector('button[type="submit"]');
  const toggles = Array.from(form.querySelectorAll('.search-modes button'));
  const status = document.getElementById('ask-status');
  const part = name => shell.querySelector('.ask-' + name);
  const progress = part('progress');
  const answer = part('answer');
  const notes = new Map(Array.from(shell.querySelectorAll('template[data-note]'),
    t => [t.dataset.note, t.content.textContent]));
  const note = key => notes.get(key) || '';

  const idleMs = 30000;
  const maxLineChars = 1048576;
  const maxStreamBytes = 4194304;

  let current = null; // the ask in flight
  let searched = page.dataset.mode === 'ask' ? null : box.value; // the words #results shows
  let sending = null; // the words of the box's or the form's search in flight
  let left = ''; // the address the page had before Ask, #results's

  // el is an element of class cls holding kids, strings as text.
  function el(tag, cls, ...kids) {
    const node = document.createElement(tag);
    if (cls !== '') {
      node.className = cls;
    }
    node.append(...kids);
    return node;
  }
  // text holds a string, stored, a model's or the script's own, as text, in a direction of its own.
  const text = (tag, cls, s) => Object.assign(el(tag, cls), {dir: 'auto', textContent: s});
  const link = (href, s, cls) => Object.assign(text('a', cls || '', s), {href: href});
  const heading = s => text('h2', '', s);
  const plural = (n, one, many) => n + ' ' + (n === 1 ? one : many);
  const documentHref = id => shell.dataset.documentPage + encodeURIComponent(id);
  const interestHref = id => shell.dataset.interestPage + encodeURIComponent(id);

  // docLink is a page named as the dashboard's lists name it, whole on hover.
  const docLink = doc => Object.assign(link(documentHref(doc.doc_id), doc.name || doc.url || '',
    doc.untitled ? 'doc-title untitled' : 'doc-title'), {title: doc.full_name || ''});

  function replaceAddress(address) {
    try {
      history.replaceState(history.state, '', address);
    } catch (err) {
      // A browser that refuses the address keeps the one it has.
    }
  }

  // keepAddress makes the page's address its question's, in Ask mode.
  function keepAddress(question) {
    const params = new URLSearchParams();
    if (question !== '') {
      params.set('q', question);
    }
    params.set('mode', 'ask');
    replaceAddress(shell.dataset.page + '?' + params.toString());
  }

  // words gives the switch, the box and its button mode's words.
  function words(mode) {
    for (const toggle of toggles) {
      const on = toggle.dataset.mode === mode;
      toggle.setAttribute('aria-pressed', on ? 'true' : 'false');
      if (on) {
        box.placeholder = toggle.dataset.placeholder;
        submit.setAttribute('aria-label', toggle.dataset.label);
        submit.querySelector('.label').textContent = toggle.dataset.label;
      }
    }
  }

  function setMode(mode) {
    if (page.dataset.mode === mode) {
      return;
    }
    page.dataset.mode = mode;
    words(mode);
    if (mode === 'ask') {
      // Every search request syncs on the page, which holds the one in flight.
      page.dispatchEvent(new CustomEvent('htmx:abort', {bubbles: true, detail: {}}));
      left = document.URL;
      keepAddress(box.value.trim());
      return;
    }
    stop(note('stopped'));
    // Ask's history entry is #results's again; a search of other words pushes its own.
    if (left !== '') {
      replaceAddress(left);
    }
    if (box.value !== searched) {
      box.dispatchEvent(new CustomEvent('curio:search'));
    }
  }

  // stop ends the ask in flight, if there is one, on lines, as end does.
  function stop(...lines) {
    if (current !== null) {
      end(current, lines);
    }
  }

  // within is p's outcome, unless idleMs pass first: then silence.
  const silence = new Error('silence');
  function within(p) {
    const idle = AbortSignal.timeout(idleMs);
    return new Promise(function (resolve, reject) {
      const expire = () => reject(silence);
      idle.addEventListener('abort', expire, {once: true});
      p.then(resolve, reject).finally(() => idle.removeEventListener('abort', expire));
    });
  }

  // refused is the note of an ask answered without its stream, by status:
  // the question (400, or 413 past the body's cap), curio starting or busy
  // with other asks (503, told apart by the problem's type), or any other.
  async function refused(resp) {
    if (resp.status === 400 || resp.status === 413) {
      return 'question';
    }
    if (resp.status !== 503) {
      return 'refused';
    }
    const problem = await resp.json().catch(() => null);
    return problem !== null && problem.type === shell.dataset.startingType ? 'starting' : 'busy';
  }

  // fail ends run on the note key, naming its request for the daemon's log.
  const fail = (run, key) => end(run, [note(key), run.id ? note('request') + ' ' + run.id : '']);

  function ask(question) {
    stop();
    for (const name of ['answer', 'notes', 'evidence', 'table', 'sources', 'unreadable', 'groups']) {
      part(name).replaceChildren();
    }
    status.textContent = '';
    const run = {controller: new AbortController(), id: '', text: '', sources: new Map(), marks: new Map(),
      unsided: false, ended: false};
    current = run;
    keepAddress(question);
    read(run, question).catch(function (err) {
      if (err === silence) {
        end(run, ['curio sent nothing for ' + idleMs / 1000 + ' s, so the answer stops here.']);
      } else {
        fail(run, 'lost');
      }
    });
  }

  async function read(run, question) {
    const resp = await within(fetch(shell.dataset.src, {method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({question: question}), signal: run.controller.signal}));
    run.id = resp.headers.get('X-Request-Id') || '';
    if (!resp.ok || !(resp.headers.get('Content-Type') || '').startsWith('application/x-ndjson')) {
      fail(run, await within(refused(resp)));
      return;
    }
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let pending = '';
    let bytes = 0;
    for (;;) {
      const chunk = await within(reader.read());
      if (current !== run) {
        return;
      }
      if (chunk.done) {
        break;
      }
      bytes += chunk.value.byteLength;
      if (bytes > maxStreamBytes) {
        end(run, ['The answer ran past the ' + maxStreamBytes / 1048576 + ' MiB this page holds; it stops here.']);
        return;
      }
      pending += decoder.decode(chunk.value, {stream: true});
      for (let at = pending.indexOf('\n'); at >= 0; at = pending.indexOf('\n')) {
        const line = pending.slice(0, at);
        pending = pending.slice(at + 1);
        if (!take(run, line) || run.ended) {
          return;
        }
      }
      if (pending.length > maxLineChars) {
        end(run, ['curio sent a line past ' + maxLineChars / 1048576 + ' MiB; the answer stops here.']);
        return;
      }
    }
    if (take(run, pending + decoder.decode()) && !run.ended) {
      fail(run, 'lost'); // the connection closed before the done event
    }
  }

  // take handles one line of the stream: false, having ended the ask, for
  // a line that isn't an event. A type it doesn't know is passed over.
  function take(run, line) {
    if (line.trim() === '') {
      return true;
    }
    let event = null;
    try {
      event = JSON.parse(line);
    } catch (err) {
      // Not JSON, refused below.
    }
    if (event === null || typeof event !== 'object' || line.length > maxLineChars) {
      end(run, ['curio sent something that isn\'t part of an answer; the answer stops here.']);
      return false;
    }
    const handle = handlers.get(event.type);
    if (handle !== undefined) {
      handle(run, event);
    }
    return true;
  }

  // end shows how run ended, once: an announcement, said once, which is
  // its first line when there is none, and its other lines as its notes.
  function end(run, lines, announcement) {
    if (current !== run) {
      return;
    }
    run.ended = true;
    current = null;
    run.controller.abort();
    progress.textContent = '';
    answer.removeAttribute('aria-busy');
    const shown = lines.filter(Boolean);
    status.textContent = announcement || shown.shift() || '';
    part('notes').replaceChildren(...shown.map(s => text('p', '', s)));
  }

  const stages = new Map([
    ['retrieving', () => 'Finding pages…'],
    ['loading', e => 'Loading ' + (e.model || 'the model') + '…'],
    ['checking', e => 'Checking ' + (e.model || 'the model') + ' before its first answer…'],
    ['waiting', () => 'Waiting for another answer to finish…'],
    ['judging', e => 'Judging ' + plural(e.pages || 0, 'page', 'pages') + ' for and against the claim…'],
    ['writing', () => 'Writing…'],
  ]);

  function showSources(run, sources) {
    const list = el('ol', '');
    for (const s of sources) {
      run.sources.set(s.n, s);
      const item = Object.assign(el('li', '', docLink(s)), {value: s.n});
      const meta = [s.site, s.saved_on ? 'saved ' + s.saved_on : ''].filter(Boolean).join(' · ');
      if (meta !== '') {
        item.append(text('div', 'ask-meta', meta));
      }
      if (s.about) {
        item.append(text('p', 'ask-meta', s.about));
      }
      list.append(item);
    }
    if (sources.length > 0) {
      part('sources').append(heading('Sources'), list);
    }
  }

  function showUnreadable(pages) {
    const list = el('ul', '', ...pages.map(p => el('li', '', docLink(p), text('span', 'ask-meta', ' — ' +
      p.reason))));
    if (pages.length > 0) {
      part('unreadable').append(heading('Found, but curio couldn\'t read them'), list);
    }
  }

  // showEvidence shows the pages for and against the claim, or why there
  // are none: judged with none either way, which the done note says.
  function showEvidence(run, e) {
    const into = part('evidence');
    if (e.status === 'none') {
      run.unsided = e.judged > 0;
      return;
    }
    if (e.status !== 'judged') {
      into.append(text('p', 'ask-meta', note('unjudged')));
      return;
    }
    for (const [title, ns] of [['Support the claim', e.supports || []], ['Contradict it', e.contradicts || []]]) {
      const list = el('ul', '');
      for (const s of ns.map(n => run.sources.get(n)).filter(s => s !== undefined)) {
        const item = el('li', '', '[' + s.n + '] ', docLink(s));
        if (s.quote) {
          item.append(text('p', 'ask-quote', '“' + s.quote + '”'));
        }
        list.append(item);
      }
      if (list.childElementCount > 0) {
        into.append(heading(title), list);
      }
    }
  }

  const rowNames = new Map([['area', 'Unlabeled area'], ['interest', 'Unlabeled interest'],
    ['other_areas', 'Other areas'], ['other_interests', 'Other interests'], ['unsorted', 'Unsorted']]);

  // rowHref is where a table row's marker leads: its area's or interest's
  // page, Unsorted's, or nowhere for a row of several.
  function rowHref(row) {
    if ((row.kind === 'area' || row.kind === 'interest') && row.id) {
      return interestHref(row.id);
    }
    return row.kind === 'unsorted' ? shell.dataset.unsortedPage : '';
  }

  function showTable(run, e) {
    const periods = e.periods || [];
    const cols = el('colgroup', '', ...['c-marker', 'c-label'].concat(periods.map(() => 'c-period'), ['c-total'])
      .map(name => el('col', name)));
    const head = el('tr', '', ...['', 'Interest'].concat(periods, ['Total'])
      .map(name => Object.assign(text('th', '', name), {scope: 'col'})));
    const body = el('tbody', '');
    const addRow = function (marker, href, label, counts, total) {
      const tr = el('tr', '', el('td', 'num', href === '' ? marker : link(href, marker)), el('td', '', text('span', '',
        label)));
      for (let i = 0; i < periods.length; i++) {
        tr.append(el('td', 'num', counts[i] === undefined ? '' : String(counts[i])));
      }
      tr.append(el('td', 'num', String(total)));
      body.append(tr);
    };
    for (const row of (e.rows || []).filter(r => r.marker)) {
      const href = rowHref(row);
      if (href !== '') {
        run.marks.set(row.marker, href);
      }
      const name = row.label || rowNames.get(row.kind) || '';
      addRow(row.marker, href, row.merged ? name + ' (' + row.merged + ')' : name, row.counts || [], row.total);
    }
    addRow(e.undated_marker, '', 'Undated', [], e.undated);
    part('table').append(heading('Your saves over time'),
      el('div', 'table-scroll', el('table', 'data', cols, el('thead', '', head), body)));
  }

  function showGroups(run, e) {
    const into = part('groups');
    for (const g of e.groups || []) {
      const label = g.label || (g.interest_id ? 'Unlabeled interest' : 'In no interest');
      const line = el('p', '', text('span', 'ask-meta', g.marker + ' '));
      if (g.interest_id) {
        run.marks.set(g.marker, interestHref(g.interest_id));
        line.append(link(interestHref(g.interest_id), label));
      } else {
        line.append(text('span', '', label));
      }
      if (g.area_label) {
        line.append(' in ', text('span', '', g.area_label));
      }
      line.append(text('span', 'ask-meta', ' · ' + plural(g.count, 'page', 'pages')));
      into.append(line, el('ul', '', ...(g.documents || []).map(doc => el('li', '', docLink(doc)))));
    }
    if (into.childElementCount > 0) {
      into.prepend(heading('Grouped by interest'));
    }
  }

  // citationHref is where a citation leads: a source's page, or a row's or
  // a group's; "" for one that leads nowhere.
  function citationHref(run, mark) {
    if (mark.marker) {
      return run.marks.get(mark.marker) || '';
    }
    const s = run.sources.get(mark.n);
    return s === undefined ? '' : documentHref(s.doc_id);
  }

  // linkCitations writes the answer again with each citation the done
  // event places linked, by code point; an invalid one has no place, and
  // stays text.
  function linkCitations(run, marks) {
    const chars = Array.from(run.text);
    const out = [];
    let at = 0;
    for (const m of marks) {
      const href = citationHref(run, m);
      if (href !== '' && m.start >= at && m.start < m.end && m.end <= chars.length) {
        out.push(chars.slice(at, m.start).join(''), link(href, chars.slice(m.start, m.end).join('')));
        at = m.end;
      }
    }
    answer.replaceChildren(...out, chars.slice(at).join(''));
  }

  const handlers = new Map([
    ['progress', function (run, e) {
      const words = stages.get(e.stage);
      if (words !== undefined) {
        progress.textContent = words(e);
      }
    }],
    ['retrieval', function (run, e) {
      showSources(run, e.sources || []);
      showUnreadable(e.unreadable || []);
    }],
    ['evidence', showEvidence],
    ['table', showTable],
    ['groups', showGroups],
    ['delta', function (run, e) {
      if (typeof e.text === 'string') {
        run.text += e.text;
        answer.setAttribute('aria-busy', 'true');
        answer.append(e.text);
      }
    }],
    ['done', function (run, e) {
      linkCitations(run, e.cited_at || []);
      const key = e.status === 'no_sources' && run.unsided ? 'unsided' : e.status;
      const lines = [e.status === 'answered' ? '' : note(key) || 'The ask ended without an answer.'];
      if (e.partial) {
        lines.push(note('partial'));
      }
      if (e.uncited) {
        lines.push(note('uncited'));
      }
      if ((e.invalid_citations || []).length > 0) {
        lines.push(note('invalid'));
      }
      end(run, lines, e.status === 'answered' ? note('answered') : '');
    }],
    ['error', run => fail(run, 'failed')],
  ]);

  words(page.dataset.mode);
  toggles.forEach(toggle => toggle.addEventListener('click', () => setMode(toggle.dataset.mode)));
  // In Ask mode the form asks, heard here before htmx or search.js hears it.
  document.addEventListener('submit', function (event) {
    if (event.target !== form || page.dataset.mode !== 'ask') {
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    const question = box.value.trim();
    if (question !== '') {
      ask(question);
    }
  }, {capture: true});
  // In Ask mode the box's searches, as it is typed in, are dropped.
  document.addEventListener('htmx:confirm', function (event) {
    const elt = event.detail.elt;
    if (page.dataset.mode === 'ask' && (elt === box || elt === form)) {
      event.preventDefault();
    }
  });
  // #results shows the words of the box's or the form's last search to land. A request's
  // events name its sender as detail.requestConfig.elt: detail.elt is the element each is
  // dispatched on, which for htmx:afterSwap is each element swapped in.
  document.addEventListener('htmx:beforeRequest', function (event) {
    if ([box, form].includes(event.detail.requestConfig.elt)) {
      sending = box.value;
    }
  });
  document.addEventListener('htmx:afterSwap', function (event) {
    if ([box, form].includes(event.detail.requestConfig.elt)) {
      searched = sending;
    }
  });
})();
