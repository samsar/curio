/* The interest map (/ui/interests/map): the latest rebuild's grouping in
   two views, drawn on canvases from GET /v1/interests/map, read once: All
   documents, each document a dot at its place on the document map,
   coloured by its area, and Zoom in, each area a disc holding its
   interests' circles, each document a dot in its interest, Unsorted a disc
   of its own. Both show one selection, as the panel and the address do.

   Its rules (TestMapScript): one strict IIFE that reads the map from its
   root's data-src and sends nothing; every address comes from the root's
   data attributes, built in Go; a stored string reaches the page only
   through textContent, the title property or fillText, in an element of
   its own with dir=auto; no inline style and no timer of its own. No
   string or regexp here holds a comment's opening.

   It reads the d3 global these modules, vendored unchanged from npm and
   loaded before it in this order, build up: d3-dispatch 3.0.1,
   d3-selection 3.0.0, d3-timer 3.0.1, d3-color 3.1.0, d3-interpolate
   3.0.1, d3-ease 3.0.1, d3-transition 3.0.1, d3-drag 3.0.0, d3-zoom 3.0.0
   and d3-quadtree 3.0.1 (https://d3js.org). All but d3-ease are under the
   ISC license:

   Copyright 2010-2021 Mike Bostock (d3-color: Copyright 2010-2022 Mike
   Bostock)

   Permission to use, copy, modify, and/or distribute this software for any
   purpose with or without fee is hereby granted, provided that the above
   copyright notice and this permission notice appear in all copies.

   THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
   WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
   MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
   ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
   WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
   ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
   OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

   d3-ease is under the BSD 3-Clause license:

   Copyright 2010-2021 Mike Bostock
   Copyright 2001 Robert Penner
   All rights reserved.

   Redistribution and use in source and binary forms, with or without
   modification, are permitted provided that the following conditions are
   met:

   * Redistributions of source code must retain the above copyright notice,
     this list of conditions and the following disclaimer.

   * Redistributions in binary form must reproduce the above copyright
     notice, this list of conditions and the following disclaimer in the
     documentation and/or other materials provided with the distribution.

   * Neither the name of the author nor the names of contributors may be
     used to endorse or promote products derived from this software without
     specific prior written permission.

   THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS
   IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO,
   THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
   PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR
   CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
   EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
   PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
   PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
   LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
   NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
   SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE. */
(function () {
  'use strict';

  // ------------------------------------------------------------------ constants
  // The one read may take 30 s: inside the daemon's 2-minute write timeout, and ample for 10 times a big library.
  // A bad value in it is quoted to 80 characters: enough to tell what it is, short enough for the status line.
  const fetchDeadlineMs = 30000;
  const maxQuoted = 80;
  // The area palette, app.css's --area-0 to --area-29: 10 hue families in 3 lightness tiers, a slot being
  // tier * paletteFamilies + family (TestMapScript holds these to the tokens).
  const paletteFamilies = 10;
  const paletteTiers = 3;
  // Documents within 1% of the map's side of each other touch: groups whose documents touch get apart hues.
  const touchShare = 0.01;
  // Backing stores follow the screen up to 2 pixels a CSS pixel: past that, the dots cost more than they show.
  const maxPixelRatio = 2;
  // A view fits the whole map into 90% of the stage, a flight's target into 86%, and an area's or Unsorted's
  // disc into 92%, aiming at the part of the stage on the screen, or the whole stage when under 35% of it shows.
  const fitPad = 0.9;
  const flightPad = 0.86;
  const discPad = 0.92;
  const minShownShare = 0.35;
  // On All documents, a flight to an area or Unsorted of over 20 documents leaves out their outer 4% each way.
  const trimShare = 0.04;
  const trimFrom = 20;
  // The zoom buttons and keys scale by 1.6: three steps take a group's neighbourhood to the whole stage.
  const zoomStep = 1.6;
  // Flights take 700 ms, a zoom step 250 ms; none takes any time under prefers-reduced-motion.
  const flightMs = 700;
  const stepMs = 250;
  // A view zooms out to half the whole map, and pans until half the stage is past its edge: never lost.
  const minZoom = 0.5;
  const panSlack = 0.5;
  // All documents zooms in to 60 times the whole map, a flight to 14 times. Zoom in zooms in to twice the whole
  // map, or on until a dot's radius is 12 px, and a flight to a document until it is 6 px.
  const maxZoomAll = 60;
  const maxFlightAll = 14;
  const maxZoomIn = 2;
  const maxDotPx = 12;
  const documentDotPx = 6;
  // A press that moves under 4 px is a click; more is a drag, which never selects.
  const clickSlop = 4;
  // A dot's radius on All documents is 1.7 px times the root of the zoom over the fit's, from 1.6 to 6 CSS px; on
  // Zoom in it is to scale, but never under 0.5 px, where a dot would vanish.
  const dotGrowth = 1.7;
  const dotMinPx = 1.6;
  const dotMaxPx = 6;
  const minScaledDotPx = 0.5;
  // All documents names areas below 2.6 times the fit's zoom, interests from there, and titles from 9 times.
  const allInterestsFrom = 2.6;
  const allTitlesFrom = 9;
  // Zoom in opens a disc spanning 30% of the stage's shorter side: its name gives way to its contents'.
  const openShare = 0.3;
  // Zoom in titles documents once a dot's radius is 5 px, and picks one from 8 px: big enough to aim at. Titled,
  // an interest's name sits over its circle on 2 lines, leaving the circle to its documents' titles.
  const titlesDotPx = 5;
  const pickDotPx = 8;
  const titledLines = 2;
  // At most 90 titles a frame, each at least 40 px wide: more is unreadable, less says nothing.
  const maxTitles = 90;
  const minTitlePx = 40;
  // Labels wrap to 3 lines at most, an interest's circle carries its name from 15 px across, tooltips cut at 300 px.
  const maxLabelLines = 3;
  const nameCirclePx = 15;
  const tipMaxPx = 300;
  // Measured widths kept, past which the cache starts over, so it can't grow without bound.
  const maxMeasured = 20000;
  // Search needs 2 characters, and lists 12 matches at most, so many of each kind.
  const minQuery = 2;
  const maxHits = 12;
  const hitCaps = {area: 4, interest: 5, document: 12};
  // The panel's lists: an interest's closest documents, Unsorted's nearest interests and documents, siblings.
  const closestShown = 8;
  const nearestShown = 8;
  const unsortedShown = 6;
  const siblingsShown = 5;
  // app.css's phone width, where the panel is a bottom sheet.
  const phoneQuery = '(max-width: 48rem)';

  // ------------------------------------------------------------------ boot
  const $ = id => document.getElementById(id);
  const root = $('interest-map');
  if (!root) {
    return;
  }
  const {src, page: mapPage, documentPage, interestPage, unsortedPage, interestsPage} = root.dataset;
  const [stage, status, panel, panelBody, panelTitle, kicker, crumbs, legend, search, hitList, sheetToggle,
    clearButton] = ['map-stage', 'map-status', 'map-panel', 'map-panel-body', 'map-panel-title', 'map-panel-kicker',
    'map-crumbs', 'map-legend', 'map-search', 'map-hits', 'map-sheet-toggle', 'map-clear'].map($);
  const tabs = {all: $('map-tab-all'), zoom: $('map-tab-zoom')};
  const canvases = {all: $('map-canvas-all'), zoom: $('map-canvas-zoom')};
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  const phone = matchMedia(phoneQuery);

  // The map's state: the model, once read; the view shown; the selection, null for the library; the
  // documents a selected group stands out with; whether a failure stopped the map.
  const state = {model: null, view: root.dataset.view === 'zoom' ? 'zoom' : 'all', sel: null, highlight: null,
    stopped: false};
  const views = {};

  // ------------------------------------------------------------------ DOM pieces
  const numbers = new Intl.NumberFormat('en-US');
  const num = n => numbers.format(n);
  const two = x => x.toFixed(2);
  const plural = (n, one, many) => num(n) + ' ' + (n === 1 ? one : many);

  // The attributes el sets, besides aria-*: none runs code, styles anything or leads anywhere.
  const settable = new Set(['class', 'id', 'title', 'role', 'type', 'tabindex', 'dir']);

  // el is a new tag with attrs from settable or aria-*, holding kids, nodes; text goes in by words and named.
  function el(tag, attrs, ...kids) {
    const node = document.createElement(tag);
    for (const [name, value] of Object.entries(attrs)) {
      if (!settable.has(name) && !name.startsWith('aria-')) {
        throw new Error('el sets no ' + name + ' attribute');
      }
      node.setAttribute(name, String(value));
    }
    node.append(...kids.filter(Boolean));
    return node;
  }

  // words is a tag holding the page's own text; named one holding a stored string, whole on hover, in a
  // direction of its own, so that a right-to-left override in it turns nothing beside it.
  const words = (tag, cls, text) => Object.assign(el(tag, {class: cls}), {textContent: text});
  const named = (tag, cls, text) => Object.assign(el(tag, {class: cls, dir: 'auto'}), {textContent: text,
    title: text});
  const plain = text => document.createTextNode(text);
  // link leads to prefix, one of the root's data-*-page, then id, escaped.
  const link = (prefix, id, label, cls) => Object.assign(words('a', cls, label), {href: prefix +
    encodeURIComponent(id)});

  // targets holds where each control of the panel, the breadcrumb and the search leads, for their one listener.
  const targets = new WeakMap();
  const go = (node, sel) => {
    targets.set(node, sel);
    return node;
  };

  // swatch is a palette slot's colour, by a class from the slot's number, never a stored string; -1's neutral.
  const swatch = slot => el('span', {class: slot >= 0 ? 'map-swatch a' + slot : 'map-swatch', 'aria-hidden': 'true'});

  // ------------------------------------------------------------------ status
  // say writes the status line, the page's one live region; nothing empties it, which hides it.
  function say(...nodes) {
    status.replaceChildren(...nodes.filter(Boolean));
    measureChrome();
  }

  const requestNote = id => id && words('span', 'map-status-meta', 'Request ' + id + ' · see curio daemon logs');
  const retryButton = () => words('button', 'btn btn-sm', 'Try again');

  // showFailure says why the map didn't load, or stopped, with Try again for what may pass.
  function showFailure(f) {
    if (f.kind === 'http' && f.status === 404) {
      say(named('span', '', problemText(f) + '. '), link(interestsPage, '', 'Interests'),
        words('span', '', ' says where the rebuilds stand.'));
    } else if (f.kind === 'http') {
      say(named('span', '', 'Couldn\'t load the map: ' + problemText(f) + '.'), requestNote(f.requestId),
        retryButton());
    } else if (f.kind === 'timeout' || f.kind === 'network') {
      say(words('span', '', 'curio-daemon didn\'t answer' + (f.kind === 'timeout' ? ' in ' + fetchDeadlineMs / 1000 +
        ' seconds' : '') + '. Is it running? (curio status)'), retryButton());
    } else {
      say(named('span', '', (f.kind === 'bad-data' ? 'The map\'s data didn\'t make sense: ' : 'The map stopped: ') +
        f.problem + '.'), requestNote(f.requestId));
    }
  }

  // problemText is what a refusal says: its problem's detail, else its title, else its status.
  function problemText(f) {
    const text = isObject(f.problem) && (f.problem.detail || f.problem.title);
    return typeof text === 'string' && text ? text : 'HTTP ' + f.status;
  }

  // stop reports an exception once, clears what its frame half drew, and draws nothing more.
  function stop(e) {
    if (!state.stopped) {
      state.stopped = true;
      Object.values(views).forEach(v => v.ctx.clearRect(0, 0, v.canvas.width, v.canvas.height));
      showFailure({kind: 'unexpected', problem: e && e.message ? e.message : String(e)});
    }
  }

  // guarded is fn, which its first exception stops: every handler and every frame runs through one.
  function guarded(fn) {
    return function (...args) {
      try {
        return state.stopped ? undefined : fn.apply(this, args);
      } catch (e) {
        return stop(e);
      }
    };
  }

  const listen = (target, type, fn, options) => target.addEventListener(type, guarded(fn), options);

  // ------------------------------------------------------------------ load
  // load reads the map once: its data, or one typed failure: http, network, timeout, or bad-data (check's).
  async function load() {
    let resp;
    let body;
    try {
      resp = await fetch(src, {signal: AbortSignal.timeout(fetchDeadlineMs)});
      body = await resp.text();
    } catch (e) {
      return {kind: e && e.name === 'TimeoutError' ? 'timeout' : 'network'};
    }
    const requestId = resp.headers.get('X-Request-Id') || '';
    let data;
    try {
      data = JSON.parse(body);
    } catch (e) {
      return resp.ok ? {kind: 'bad-data', problem: 'it isn\'t JSON', requestId} :
        {kind: 'http', status: resp.status, problem: null, requestId};
    }
    const problem = resp.ok ? check(data) : '';
    return !resp.ok ? {kind: 'http', status: resp.status, problem: data, requestId} :
      problem ? {kind: 'bad-data', problem, requestId} : {kind: 'ok', data};
  }

  const finite = x => typeof x === 'number' && Number.isFinite(x);
  const isObject = x => x !== null && typeof x === 'object' && !Array.isArray(x);
  const isText = x => typeof x === 'string';
  const index = n => x => Number.isInteger(x) && x >= -1 && x < n;
  const point = p => isObject(p) && finite(p.x) && finite(p.y);
  const circle = c => point(c) && finite(c.r) && c.r > 0;
  const fits = new Set(['member', 'loose', 'unsorted', 'new']);

  // check is the first thing wrong with the map's response, and where, or '': one pass, before anything is
  // drawn. A flat map has no area, so its every area index is -1.
  function check(data) {
    if (!isObject(data) || !isObject(data.map) || !Array.isArray(data.areas) || !Array.isArray(data.interests) ||
        !isObject(data.documents) || data.shape !== 'flat' && data.shape !== 'areas') {
      return 'it isn\'t an interest map';
    }
    const m = data.map;
    if (!finite(m.extent) || m.extent <= 0 || !finite(m.dot_radius) || m.dot_radius <= 0 || !circle(m.unsorted)) {
      return 'its extent, dot radius or Unsorted\'s disc isn\'t a positive number';
    }
    if (data.shape === 'flat' && data.areas.length) {
      return 'a flat map has ' + plural(data.areas.length, 'area', 'areas');
    }
    const [na, ni] = [data.areas.length, data.interests.length];
    const group = g => isObject(g) && isText(g.id) && (g.label === undefined || isText(g.label)) &&
      Number.isInteger(g.size) && finite(g.cohesion) && circle(g.zoom) && point(g.anchor);
    const similar = s => isObject(s) && Number.isInteger(s.interest) && s.interest >= 0 && s.interest < ni &&
      finite(s.cosine);
    for (const [name, list, valid] of [['areas', data.areas, group], ['interests', data.interests, it => group(it) &&
      index(na)(it.area) && Array.isArray(it.similar) && it.similar.every(similar)]]) {
      const bad = list.findIndex(g => !valid(g));
      if (bad >= 0) {
        return name + '[' + bad + '] isn\'t one the map can draw';
      }
    }
    const columns = {id: isText, title: isText, host: isText, interest: index(ni), nearest: index(ni), area: index(na),
      fit: f => fits.has(f), similarity: finite, mx: finite, my: finite, zx: finite, zy: finite};
    const docs = data.documents;
    for (const [name, valid] of Object.entries(columns)) {
      if (!Array.isArray(docs[name]) || !Array.isArray(docs.id) || docs[name].length !== docs.id.length) {
        return 'documents.' + name + ' isn\'t a column as long as documents.id';
      }
      const bad = docs[name].findIndex(x => !valid(x));
      if (bad >= 0) {
        return 'documents.' + name + '[' + bad + '] is ' + quote(docs[name][bad]);
      }
    }
    return '';
  }

  // quote is x as JSON, cut to maxQuoted characters.
  function quote(x) {
    const chars = Array.from(String(JSON.stringify(x)));
    return chars.length > maxQuoted ? chars.slice(0, maxQuoted).join('') + '…' : chars.join('');
  }

  // ------------------------------------------------------------------ model
  // buildModel is what the views, the panel and the search read, built once from a checked response: each
  // group's documents, closest first; each top-level group's (an area, or a flat shape's interest), and those in
  // none, drawn in the neutral; the new ones; each view's quadtree; the colour slots; the search's index.
  function buildModel(data) {
    const docs = data.documents;
    const flat = data.shape === 'flat';
    const m = {flat, docs, n: docs.id.length, areas: data.areas, interests: data.interests,
      extent: data.map.extent, dotRadius: data.map.dot_radius, disc: data.map.unsorted,
      interestDocs: data.interests.map(() => []), areaDocs: data.areas.map(() => []), unsortedDocs: [],
      areaInterests: data.areas.map(() => []), neutralDocs: [], newDocs: []};
    m.top = flat ? m.interests : m.areas;
    m.topOf = flat ? docs.interest : docs.area;
    m.groupDocs = m.top.map(() => []);
    for (let d = 0; d < m.n; d++) {
      (docs.interest[d] >= 0 ? m.interestDocs[docs.interest[d]] : m.unsortedDocs).push(d);
      (m.topOf[d] >= 0 ? m.groupDocs[m.topOf[d]] : m.neutralDocs).push(d);
      if (docs.area[d] >= 0) {
        m.areaDocs[docs.area[d]].push(d);
      }
      if (docs.fit[d] === 'new') {
        m.newDocs.push(d);
      }
    }
    const closest = (a, b) => (docs.fit[a] === 'loose') - (docs.fit[b] === 'loose') ||
      docs.similarity[b] - docs.similarity[a];
    m.interestDocs.forEach(list => list.sort(closest));
    m.unsortedDocs.sort((a, b) => (docs.nearest[b] >= 0) - (docs.nearest[a] >= 0) ||
      docs.similarity[b] - docs.similarity[a]);
    m.interests.forEach((it, i) => it.area >= 0 && m.areaInterests[it.area].push(i));
    m.interestsBySize = m.interests.map((_, i) => i).sort((a, b) => m.interests[b].size - m.interests[a].size);
    const all = docs.id.map((_, d) => d);
    m.allTree = d3.quadtree().x(d => docs.mx[d]).y(d => docs.my[d]).addAll(all);
    m.zoomTree = d3.quadtree().x(d => docs.zx[d]).y(d => docs.zy[d]).addAll(all);
    m.slots = colourSlots(m);
    m.index = searchIndex(m);
    return m;
  }

  // colourSlots gives each top-level group a palette slot, in the response's order, largest first: the hue
  // family whose groups already coloured touch it least on the document map, ties to the family used least so
  // far, then the lower; its tier, how many groups already hold that family. Touch is how many pairs of their
  // documents lie within touchShare of the map's side of each other, counted under the later group. On the
  // owner's map no two areas whose documents touch share a family, where plain order puts 53 such pairs in one.
  function colourSlots(m) {
    const touch = m.top.map(() => new Map());
    const r = touchShare * m.extent;
    const {mx, my} = m.docs;
    m.groupDocs.forEach((list, a) => list.forEach(d => visit(m.allTree, mx[d] - r, my[d] - r, mx[d] + r, my[d] + r,
      e => {
        const b = m.topOf[e];
        if (e > d && b >= 0 && b !== a && (mx[e] - mx[d]) ** 2 + (my[e] - my[d]) ** 2 <= r * r) {
          const [later, earlier] = a > b ? [a, b] : [b, a];
          touch[later].set(earlier, (touch[later].get(earlier) || 0) + 1);
        }
      })));
    const used = new Array(paletteFamilies).fill(0);
    const family = [];
    return m.top.map((_, g) => {
      const weight = new Array(paletteFamilies).fill(0);
      touch[g].forEach((w, earlier) => {
        weight[family[earlier]] += w;
      });
      let best = 0;
      for (let f = 1; f < paletteFamilies; f++) {
        best = weight[f] < weight[best] || weight[f] === weight[best] && used[f] < used[best] ? f : best;
      }
      family[g] = best;
      return (used[best]++ % paletteTiers) * paletteFamilies + best;
    });
  }

  // visit calls fn with every document of tree in the box (x0, y0)-(x1, y1), and some just outside it.
  function visit(tree, x0, y0, x1, y1, fn) {
    tree.visit((node, nx0, ny0, nx1, ny1) => {
      for (let leaf = node.length ? null : node; leaf; leaf = leaf.next) {
        fn(leaf.data);
      }
      return nx0 > x1 || nx1 < x0 || ny0 > y1 || ny1 < y0;
    });
  }

  // slotOf is a selection's colour slot: its top-level group's, -1 for Unsorted's neutral.
  function slotOf(m, sel) {
    const g = sel.kind === 'document' ? m.topOf[sel.i] : sel.kind === 'unsorted' ? -1 :
      sel.kind === 'area' || m.flat ? sel.i : m.interests[sel.i].area;
    return g >= 0 ? m.slots[g] : -1;
  }

  // The kinds the search ranks, in their order, and what the page calls them.
  const kindRank = {area: 0, interest: 1, document: 2};
  const kindNames = {area: 'Area', interest: 'Interest', document: 'Document', unsorted: 'Unsorted'};

  // searchIndex is every area, interest and document with the text a query matches: a name, a document's host.
  function searchIndex(m) {
    const groups = kind => (kind === 'area' ? m.areas : m.interests).map((g, i) => ({sel: {kind, i},
      name: groupName(m, kind, i), text: (g.label || '').toLowerCase(), meta: tipOf(m, {kind, i}).meta}));
    return groups('area').concat(groups('interest'), m.docs.id.map((_, d) => ({sel: {kind: 'document', i: d},
      name: m.docs.title[d], meta: m.docs.host[d], text: (m.docs.title[d] + ' ' + m.docs.host[d]).toLowerCase()})));
  }

  // groupName is an area's or an interest's label, or what an unlabeled one is called.
  const groupOf = (m, sel) => (sel.kind === 'area' ? m.areas : m.interests)[sel.i];
  const groupName = (m, kind, i) => groupOf(m, {kind, i}).label || 'Unlabeled ' + kind;
  const isUnlabeled = (m, sel) => (sel.kind === 'area' || sel.kind === 'interest') && !groupOf(m, sel).label;

  // ------------------------------------------------------------------ selection
  const same = (a, b) => a === b || !!a && !!b && a.kind === b.kind && a.i === b.i;

  // parentOf is the selection a level up: a document's interest or Unsorted, an interest's area, and the
  // library above an area, Unsorted and a flat interest.
  function parentOf(m, sel) {
    if (sel && sel.kind === 'document') {
      const i = m.docs.interest[sel.i];
      return i >= 0 ? {kind: 'interest', i} : {kind: 'unsorted'};
    }
    return sel && sel.kind === 'interest' && m.interests[sel.i].area >= 0 ?
      {kind: 'area', i: m.interests[sel.i].area} : null;
  }

  // nameOf is what a selection is called.
  function nameOf(m, sel) {
    if (!sel || sel.kind === 'unsorted') {
      return sel ? 'Unsorted' : 'Library';
    }
    return sel.kind === 'document' ? m.docs.title[sel.i] : groupName(m, sel.kind, sel.i);
  }

  // selectionParam is a selection as the address's select names it, in mapHref's form; '' for the library.
  function selectionParam(m, sel) {
    if (!sel || sel.kind === 'unsorted') {
      return sel ? 'unsorted' : '';
    }
    return sel.kind + ':' + (sel.kind === 'document' ? m.docs.id[sel.i] : groupOf(m, sel).id);
  }

  // kindAndID splits a selection param at its first colon, as ParseMapQuery does.
  const kindAndID = param => [param.slice(0, param.indexOf(':')), param.slice(param.indexOf(':') + 1)];

  // resolve is the selection param names in ParseMapQuery's form: undefined when it isn't on the map.
  function resolve(m, param) {
    const [kind, id] = kindAndID(param);
    const ids = {area: m.areas.map(a => a.id), interest: m.interests.map(it => it.id), document: m.docs.id}[kind];
    const i = ids ? ids.indexOf(id) : -1;
    return param === 'unsorted' ? {kind: param} : i >= 0 ? {kind, i} : undefined;
  }

  // focusArea is the area a selection is in, which Zoom in keeps bright: -1 for none, -2 for Unsorted.
  function focusArea(m, sel) {
    const inInterest = sel && sel.kind === 'document' && m.docs.interest[sel.i] >= 0;
    return !sel || m.flat ? -1 : sel.kind === 'area' ? sel.i : sel.kind === 'interest' ? m.interests[sel.i].area :
      inInterest ? m.docs.area[sel.i] : -2;
  }

  // membersOf are the documents a selected group holds.
  function membersOf(m, sel) {
    return sel.kind === 'unsorted' ? m.unsortedDocs : (sel.kind === 'area' ? m.areaDocs : m.interestDocs)[sel.i];
  }

  // highlighted marks the documents a selected group stands out with on All documents; null for none.
  function highlighted(m, sel) {
    if (!sel || sel.kind === 'document') {
      return null;
    }
    const on = new Uint8Array(m.n);
    for (const d of membersOf(m, sel)) {
      on[d] = 1;
    }
    return on;
  }

  // ------------------------------------------------------------------ theme and text
  // The theme's colours and font, read from app.css's tokens, and again on every change of theme.
  let theme = {};

  function readTheme() {
    const css = getComputedStyle(document.documentElement);
    const token = name => css.getPropertyValue(name).trim();
    theme = {surface: token('--surface'), disc: token('--surface-2'), line: token('--border-strong'),
      text: token('--text'), text2: token('--text-2'), text3: token('--text-3'), neutral: token('--neutral-dot'),
      accent: token('--accent'), accentText: token('--accent-text'), font: token('--font'),
      area: Array.from({length: paletteFamilies * paletteTiers}, (_, s) => token('--area-' + s))};
    measured.clear();
  }

  const measurer = document.createElement('canvas').getContext('2d');
  const measured = new Map();
  const fontOf = (weight, px) => weight + ' ' + px + 'px ' + theme.font;

  // width is text's width in font, measured once until the cache starts over.
  function width(text, font) {
    const key = font + '\u0001' + text;
    if (!measured.has(key)) {
      if (measured.size >= maxMeasured) {
        measured.clear();
      }
      measurer.font = font;
      measured.set(key, measurer.measureText(text).width);
    }
    return measured.get(key);
  }

  // cut is text, cut with an ellipsis to fit max px: between code points, as quote cuts, never inside an emoji.
  function cut(text, font, max) {
    if (width(text, font) <= max) {
      return text;
    }
    const chars = Array.from(text);
    let [lo, hi] = [0, chars.length];
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      [lo, hi] = width(chars.slice(0, mid).join('') + '…', font) <= max ? [mid, hi] : [lo, mid - 1];
    }
    return chars.slice(0, lo).join('').trimEnd() + '…';
  }

  // wrap is text in lines of at most max px, lines of them at most, the last cut.
  function wrap(text, font, max, lines) {
    const out = [''];
    for (const word of text.split(/\s+/)) {
      const line = out[out.length - 1];
      if (line && width(line + ' ' + word, font) > max) {
        out.push(word);
      } else {
        out[out.length - 1] = line ? line + ' ' + word : word;
      }
    }
    if (out.length > lines) {
      out.splice(lines - 1, out.length, out.slice(lines - 1).join(' '));
    }
    return out.map(l => cut(l, font, max));
  }

  // ------------------------------------------------------------------ views
  // The stage's controls, in the canvases' coordinates, measured as they or the stage change: labels keep off them.
  let chrome = [];

  function measureChrome() {
    const at = stage.getBoundingClientRect();
    chrome = [crumbs, stage.querySelector('.map-zoom'), legend, status].map(n => n.getBoundingClientRect())
      .filter(r => r.width > 0 && r.height > 0)
      .map(r => [r.left - at.left - 4, r.top - at.top - 4, r.right - at.left + 4, r.bottom - at.top + 4]);
  }

  // makeView is a view on its canvas: its size, its d3.zoom, its frames, drawn one requestAnimationFrame at a
  // time on a dirty flag, its fitting and its flights. spec draws it (world, maxK, draw, pick, click, frame).
  // touched is whether it was moved off the whole map's fit, by a gesture, a zoom button or key, or a flight to
  // a selection: a resize keeps such a view's middle and zoom, and fits the whole map again otherwise. flight is
  // the last flight land started, which a resize sends on while it is on its way.
  function makeView(name, spec) {
    const canvas = canvases[name];
    const v = Object.assign({name, canvas, ctx: canvas.getContext('2d'), t: d3.zoomIdentity, w: 0, h: 0, ratio: 1,
      fitK: 1, touched: false, flight: null, queued: false, hover: null, tip: null, labelHits: []}, spec);
    v.zoom = d3.zoom().clickDistance(clickSlop).on('zoom', guarded(e => {
      v.t = e.transform;
      if (e.sourceEvent) {
        Object.assign(v, {touched: true, tip: null});
      }
      request(v);
    }));
    d3.select(canvas).call(v.zoom).on('dblclick.zoom', null);
    // A double click's second click is its own: the first already went up a level.
    listen(canvas, 'click', e => e.detail <= 1 && v.click(v, v.pick(v, ...d3.pointer(e, canvas))));
    listen(canvas, 'dblclick', e => !v.pick(v, ...d3.pointer(e, canvas)) && land(v, null, flightMs));
    listen(canvas, 'pointermove', e => e.pointerType === 'mouse' && hoverAt(v, ...d3.pointer(e, canvas)));
    listen(canvas, 'pointerleave', () => hoverAt(v));
    // Safari pinches with gesture events, which scale the page; its ctrl+wheel, which d3 zooms by, carries the
    // pinch too, so the gestures are only stopped.
    ['gesturestart', 'gesturechange', 'gestureend'].forEach(type => canvas.addEventListener(type,
      e => e.preventDefault()));
    return v;
  }

  // request draws v in the next frame, once however often it is asked, while it is the view shown.
  function request(v) {
    if (!v.queued && !state.stopped && v.name === state.view) {
      v.queued = true;
      requestAnimationFrame(guarded(() => {
        v.queued = false;
        draw(v);
      }));
    }
  }

  function draw(v) {
    const {ctx, w, h, ratio} = v;
    ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
    Object.assign(ctx, {globalAlpha: 1, fillStyle: theme.surface});
    ctx.fillRect(0, 0, w, h);
    Object.assign(v, {boxes: chrome.slice(), labelHits: []});
    v.draw(v, ctx);
    drawTip(v, ctx);
  }

  // resize follows the canvas's size and pixel ratio, sizing its backing store by its attributes, and the controls
  // over the stage, which move with it: fitted again while untouched, and otherwise about the canvas's middle; a
  // flight on its way flies on, for the time it has left, to its frame as the stage now is. It draws at once:
  // sizing the backing store empties it, and the frame being painted mustn't show it empty.
  function resize(v) {
    measureChrome();
    const r = v.canvas.getBoundingClientRect();
    const ratio = Math.min(devicePixelRatio || 1, maxPixelRatio);
    if (r.width === v.w && r.height === v.h && ratio === v.ratio) {
      return;
    }
    const [w0, h0, t0, flight] = [v.w, v.h, v.t, d3.active(v.canvas) && v.flight];
    Object.assign(v, {w: r.width, h: r.height, ratio});
    v.canvas.width = Math.max(1, Math.round(r.width * ratio));
    v.canvas.height = Math.max(1, Math.round(r.height * ratio));
    const world = v.world(v);
    v.fitK = fitPad * Math.min(v.w / Math.max(world.x1 - world.x0, 1e-6), v.h / Math.max(world.y1 - world.y0, 1e-6));
    const [sw, sh] = [(world.x1 - world.x0) * panSlack, (world.y1 - world.y0) * panSlack];
    v.zoom.scaleExtent([v.fitK * minZoom, v.maxK(v)])
      .translateExtent([[world.x0 - sw, world.y0 - sh], [world.x1 + sw, world.y1 + sh]]);
    jump(v, !v.touched || !w0 ? fitAll(v) : centreOn(v, (w0 / 2 - t0.x) / t0.k, (h0 / 2 - t0.y) / t0.k, t0.k,
      {x0: 0, y0: 0, x1: v.w, y1: v.h}));
    if (flight) {
      land(v, flight.sel, flight.end - d3.now());
    }
    draw(v);
  }

  // visible is the part of the stage on the screen, which fits and flights aim at: below the sticky header,
  // above the window's foot or a phone's sheet; the whole stage when little of it shows.
  function visible(v) {
    const at = v.canvas.getBoundingClientRect();
    const header = document.querySelector('header.site');
    const bottom = phone.matches ? panel.getBoundingClientRect().top : document.documentElement.clientHeight;
    const y0 = Math.max(0, (header ? header.getBoundingClientRect().bottom : 0) - at.top);
    const y1 = Math.min(v.h, bottom - at.top);
    return y1 - y0 >= minShownShare * v.h ? {x0: 0, y0, x1: v.w, y1} : {x0: 0, y0: 0, x1: v.w, y1: v.h};
  }

  // fitTransform fits box, in world units, into pad of the visible stage, zoomed in no further than maxK.
  function fitTransform(v, box, pad, maxK) {
    const s = visible(v);
    const k = pad * Math.min((s.x1 - s.x0) / Math.max(box.x1 - box.x0, 1e-6),
      (s.y1 - s.y0) / Math.max(box.y1 - box.y0, 1e-6));
    return centreOn(v, (box.x0 + box.x1) / 2, (box.y0 + box.y1) / 2, Math.min(k, maxK || Infinity));
  }

  // centreOn is the transform at zoom k, held to the extent, with (x, y) in the middle of s, a box of the stage:
  // the part on the screen, unless another is given.
  function centreOn(v, x, y, k, s = visible(v)) {
    k = Math.max(v.fitK * minZoom, Math.min(k, v.maxK(v)));
    return d3.zoomIdentity.translate((s.x0 + s.x1) / 2 - k * x, (s.y0 + s.y1) / 2 - k * y).scale(k);
  }

  const fitAll = v => fitTransform(v, v.world(v), fitPad);

  // land flies v to sel's frame, the whole map's for none, in ms (at once under reduced motion): touched on a
  // selection, untouched on the whole map.
  function land(v, sel, ms) {
    const fly = ms > 0 && !reducedMotion.matches;
    Object.assign(v, {touched: !!sel, flight: fly ? {sel, end: d3.now() + ms} : null});
    if (fly) {
      d3.select(v.canvas).transition().duration(ms).call(v.zoom.transform, v.frame(v, sel));
    } else {
      jump(v, v.frame(v, sel));
    }
  }

  const jump = (v, t) => d3.select(v.canvas).interrupt().call(v.zoom.transform, t);

  // zoomBy scales v about its middle, for a zoom button or key: the user's zoom, as a wheel's is.
  function zoomBy(v, factor) {
    Object.assign(v, {touched: true, flight: null});
    d3.select(v.canvas).transition().duration(reducedMotion.matches ? 0 : stepMs).call(v.zoom.scaleBy, factor);
  }

  // bounds boxes list's places (xs, ys), less its outer trimShare each way with trim.
  function bounds(list, xs, ys, trim) {
    const [sx, sy] = [xs, ys].map(at => list.map(d => at[d]).sort((a, b) => a - b));
    const [lo, hi] = trim && list.length > trimFrom ? [Math.floor(list.length * trimShare),
      Math.ceil(list.length * (1 - trimShare)) - 1] : [0, list.length - 1];
    return {x0: sx[lo], y0: sy[lo], x1: sx[hi], y1: sy[hi]};
  }

  const grow = (b, by) => ({x0: b.x0 - by, y0: b.y0 - by, x1: b.x1 + by, y1: b.y1 + by});
  const around = c => ({x0: c.x - c.r, y0: c.y - c.r, x1: c.x + c.r, y1: c.y + c.r});

  // hoverAt rings what lies under the pointer at (x, y), and names it in the tooltip; nothing without one.
  function hoverAt(v, x, y) {
    const hit = x === undefined ? null : v.pick(v, x, y);
    v.canvas.classList.toggle('is-pointing', !!hit);
    Object.assign(v, {hover: hit, tip: hit && Object.assign({x, y}, tipOf(state.model, hit))});
    request(v);
  }

  // tipOf is a tooltip's text: a name, and a line about it.
  function tipOf(m, sel) {
    const docs = m.docs;
    if (sel.kind === 'document') {
      const [i, near] = [docs.interest[sel.i], docs.nearest[sel.i]];
      return {name: docs.title[sel.i], meta: docs.host[sel.i] + ' · ' + (i >= 0 ? groupName(m, 'interest', i) :
        'Unsorted' + (near >= 0 ? ' · nearest ' + groupName(m, 'interest', near) : ''))};
    }
    if (sel.kind === 'unsorted') {
      return {name: 'Unsorted', meta: plural(m.unsortedDocs.length, 'document', 'documents') + ' in no interest'};
    }
    const g = groupOf(m, sel);
    return {name: groupName(m, sel.kind, sel.i), meta: (sel.kind === 'area' ? plural(m.areaInterests[sel.i].length,
      'interest', 'interests') + ' · ' : '') + plural(g.size, 'document', 'documents') +
      (sel.kind === 'interest' && g.area >= 0 ? ' · ' + groupName(m, 'area', g.area) : '')};
  }

  // drawTip draws the tooltip by the pointer, turned at the canvas's edges: on the canvas, it needs no style.
  function drawTip(v, ctx) {
    if (!v.tip) {
      return;
    }
    const [nameFont, metaFont] = [fontOf(600, 13), fontOf(400, 12)];
    const [name, meta] = [cut(v.tip.name, nameFont, tipMaxPx), cut(v.tip.meta, metaFont, tipMaxPx)];
    const [bw, bh] = [Math.max(width(name, nameFont), width(meta, metaFont)) + 18, 42];
    const x = Math.max(4, v.tip.x + 14 + bw > v.w - 4 ? v.tip.x - bw - 14 : v.tip.x + 14);
    const y = Math.max(4, v.tip.y + 14 + bh > v.h - 4 ? v.tip.y - bh - 14 : v.tip.y + 14);
    Object.assign(ctx, {globalAlpha: 1, fillStyle: theme.surface, lineWidth: 1, strokeStyle: theme.line,
      textAlign: 'left', textBaseline: 'top'});
    ctx.beginPath();
    ctx.roundRect(x, y, bw, bh, 8);
    ctx.fill();
    ctx.stroke();
    write(ctx, name, x + 9, y + 6, nameFont, theme.text);
    write(ctx, meta, x + 9, y + 24, metaFont, theme.text3);
  }

  function write(ctx, line, x, y, font, colour) {
    Object.assign(ctx, {font, fillStyle: colour});
    ctx.fillText(line, x, y);
  }

  // label places lines at (x, y), centred or from x, if they meet no placed label or control; sel's is clicked.
  function label(v, pending, lines, x, y, font, colour, lineHeight, sel, left) {
    const w = Math.max(...lines.map(l => width(l, font)));
    const [x0, x1] = left ? [x - 1, x + w + 1] : [x - w / 2 - 2, x + w / 2 + 2];
    const b = [x0, y - 1, x1, y + lines.length * lineHeight + 1];
    if (b[0] < 2 || b[2] > v.w - 2 || b[1] < 1 || b[3] > v.h - 1 ||
        v.boxes.some(o => b[0] < o[2] && o[0] < b[2] && b[1] < o[3] && o[1] < b[3])) {
      return false;
    }
    v.boxes.push(b);
    pending.push({lines, x, y, font, colour, lineHeight, left});
    if (sel) {
      v.labelHits.push({b, sel});
    }
    return true;
  }

  // writeLabels draws the placed labels, each over a halo of the map's background.
  function writeLabels(ctx, pending) {
    Object.assign(ctx, {globalAlpha: 1, textBaseline: 'top', lineJoin: 'round', lineWidth: 3.5,
      strokeStyle: theme.surface});
    for (const p of pending) {
      ctx.textAlign = p.left ? 'left' : 'center';
      p.lines.forEach((line, i) => {
        ctx.font = p.font;
        ctx.strokeText(line, p.x, p.y + i * p.lineHeight);
        write(ctx, line, p.x, p.y + i * p.lineHeight, p.font, p.colour);
      });
    }
  }

  function labelAt(v, x, y) {
    const hit = v.labelHits.find(({b}) => x >= b[0] && x <= b[2] && y >= b[1] && y <= b[3]);
    return hit ? hit.sel : null;
  }

  // titles places list's titles, the selected one's first, at most maxTitles, each cut to max px.
  function titles(v, pending, m, list, at, max) {
    const font = fontOf(500, 11);
    const sel = state.sel && state.sel.kind === 'document' ? state.sel.i : -1;
    let placed = 0;
    for (const d of sel >= 0 ? [sel].concat(list) : list) {
      const [x, y] = at(d);
      const room = Math.min(max, v.w - 4 - x);
      if (room >= minTitlePx && y >= 0 && y <= v.h && label(v, pending, [cut(m.docs.title[d], font, room)], x, y, font,
        d === sel ? theme.accentText : theme.text2, 14, null, true) && ++placed >= maxTitles) {
        return;
      }
    }
  }

  // dots draws list's documents that keep accepts, of radius r at screen places xy: filled, or rings ring wide.
  function dots(ctx, v, list, xy, r, keep, ring) {
    ctx.beginPath();
    for (const d of list) {
      const [x, y] = xy(d);
      if (keep(d) && x > -r && x < v.w + r && y > -r && y < v.h + r) {
        ctx.moveTo(x + r, y);
        ctx.arc(x, y, r, 0, 2 * Math.PI);
      }
    }
    if (ring) {
      ctx.lineWidth = ring;
    }
    ctx[ring ? 'stroke' : 'fill']();
  }

  // documentPasses draws the documents as both views mark them: members and new ones filled in their group's
  // colour, loose fits as rings of it, those of no group (Unsorted's) in the neutral, and an accent ring
  // around each new one. alphaOf is a document's opacity, one of alphas, drawn in turn, the faded first.
  function documentPasses(ctx, v, m, xy, r, alphaOf, alphas) {
    const fit = m.docs.fit;
    for (const alpha of alphas) {
      const at = d => alphaOf(d) === alpha;
      Object.assign(ctx, {globalAlpha: alpha, fillStyle: theme.neutral});
      dots(ctx, v, m.neutralDocs, xy, r, at);
      m.groupDocs.forEach((list, g) => {
        ctx.fillStyle = ctx.strokeStyle = theme.area[m.slots[g]];
        dots(ctx, v, list, xy, r, d => at(d) && fit[d] !== 'loose');
        dots(ctx, v, list, xy, r * 0.8, d => at(d) && fit[d] === 'loose', Math.max(r * 0.45, 1));
      });
      ctx.strokeStyle = theme.accent;
      dots(ctx, v, m.newDocs, xy, r + 1.5, at, 1.5);
    }
  }

  // ring circles a place on the screen.
  function ring(ctx, x, y, r, colour, w) {
    Object.assign(ctx, {globalAlpha: 1, lineWidth: w, strokeStyle: colour});
    ctx.beginPath();
    ctx.arc(x, y, r, 0, 2 * Math.PI);
    ctx.stroke();
  }

  const key = (cls, text) => el('span', {class: 'map-key-item'}, el('i', {class: 'map-key ' + cls,
    'aria-hidden': 'true'}), plain(text));

  // ------------------------------------------------------------------ All documents
  const allView = {
    world(v) {
      const m = state.model;
      return m.n ? grow(bounds(m.allTree.data(), m.docs.mx, m.docs.my, false), 0.02 * m.extent) :
        {x0: 0, y0: 0, x1: m.extent, y1: m.extent};
    },
    maxK: v => v.fitK * maxZoomAll,
    dotPx: v => Math.min(dotMaxPx, Math.max(dotMinPx, dotGrowth * Math.sqrt(v.t.k / v.fitK))),
    draw(v, ctx) {
      const m = state.model;
      const {t, hover} = v;
      const r = v.dotPx(v);
      const xy = d => [m.docs.mx[d] * t.k + t.x, m.docs.my[d] * t.k + t.y];
      const hi = state.highlight;
      documentPasses(ctx, v, m, xy, r, hi ? d => (hi[d] ? 0.95 : 0.17) : () => 0.88, hi ? [0.17, 0.95] : [0.88]);
      const mark = (s, colour, w) => s && s.kind === 'document' && ring(ctx, ...xy(s.i), r + 3, colour, w);
      mark(hover, theme.text3, 1.5);
      mark(state.sel, theme.accent, 2.5);
      allLabels(v, ctx, m, r);
    },
    pick(v, x, y) {
      const d = state.model.allTree.find(...v.t.invert([x, y]), (v.dotPx(v) + 5) / v.t.k);
      return labelAt(v, x, y) || (d === undefined ? null : {kind: 'document', i: d});
    },
    // click selects a document where it is, and flies to a group named by its label; empty space goes up.
    click(v, hit) {
      choose(hit || parentOf(state.model, state.sel), {fly: !hit || hit.kind !== 'document'});
    },
    // frame is where a flight lands: a group's documents (an area's less outliers); a document, told apart.
    frame(v, sel) {
      const m = state.model;
      if (sel && sel.kind === 'document') {
        return centreOn(v, m.docs.mx[sel.i], m.docs.my[sel.i], Math.max(v.t.k, v.fitK * allTitlesFrom));
      }
      const list = sel ? membersOf(m, sel) : [];
      return list.length ? fitTransform(v, grow(bounds(list, m.docs.mx, m.docs.my, sel.kind !== 'interest'), 10),
        flightPad, v.fitK * maxFlightAll) : fitAll(v);
    },
    legend: m => [words('span', '', 'Each dot is a document, coloured by its ' + (m.flat ? 'interest' : 'area')),
      key('map-key-unsorted', 'unsorted'), key('map-key-loose', 'loose fit'), key('map-key-new', 'new')],
    about: m => 'Every document is a dot near the documents most like it, coloured by its ' +
      (m.flat ? 'interest' : 'area') + ': grey dots are unsorted, rings loose fits, ringed dots new since the rebuild.',
  };

  // allLabels names what the zoom allows on All documents: the selected group first, then the areas (the
  // interests, once zoomed in or in the flat shape) at their anchors, largest first, then titles, middle first.
  function allLabels(v, ctx, m, r) {
    const t = v.t;
    const [sx, sy] = [x => x * t.k + t.x, y => y * t.k + t.y];
    const pending = [];
    const sel = state.sel;
    const focus = focusArea(m, sel);
    const place = (kind, i, weight, px, w, colour) => {
      const g = groupOf(m, {kind, i});
      const font = fontOf(weight, px);
      const lines = wrap(groupName(m, kind, i), font, w, maxLabelLines);
      label(v, pending, lines, sx(g.anchor.x), sy(g.anchor.y) - lines.length * px * 0.6, font, colour, px * 1.2,
        {kind, i});
    };
    if (sel && (sel.kind === 'area' || sel.kind === 'interest')) {
      place(sel.kind, sel.i, 650, 13, 180, theme.accentText);
    }
    const dim = a => (focus >= 0 && a !== focus ? theme.text3 : theme.text);
    if (!m.flat && t.k / v.fitK < allInterestsFrom) {
      m.areas.forEach((_, a) => place('area', a, 650, 12.5, 150, dim(a)));
    } else {
      m.interestsBySize.forEach(i => place('interest', i, 600, 11.5, 140, dim(m.interests[i].area)));
    }
    if (t.k / v.fitK >= allTitlesFrom) {
      const [[x0, y0], [x1, y1]] = [t.invert([0, 0]), t.invert([v.w, v.h])];
      const shown = [];
      visit(m.allTree, x0, y0, x1, y1, d => shown.push(d));
      const mid = d => (sx(m.docs.mx[d]) - v.w / 2) ** 2 + (sy(m.docs.my[d]) - v.h / 2) ** 2;
      titles(v, pending, m, shown.sort((a, b) => mid(a) - mid(b)), d => [sx(m.docs.mx[d]) + r + 4,
        sy(m.docs.my[d]) - 7], 190);
    }
    writeLabels(ctx, pending);
  }

  // ------------------------------------------------------------------ Zoom in
  const zoomView = {
    world(v) {
      const m = state.model;
      const b = [m.disc, ...m.top.map(g => g.zoom)].map(around).reduce((a, c) => ({x0: Math.min(a.x0, c.x0),
        y0: Math.min(a.y0, c.y0), x1: Math.max(a.x1, c.x1), y1: Math.max(a.y1, c.y1)}));
      return {x0: b.x0, y0: b.y0 - 0.03 * m.extent, x1: b.x1, y1: b.y1};
    },
    maxK: v => Math.max(v.fitK * maxZoomIn, maxDotPx / state.model.dotRadius),
    draw(v, ctx) {
      const m = state.model;
      const {t} = v;
      const sel = state.sel;
      const focus = focusArea(m, sel);
      const shade = a => (focus === -1 || a === focus ? 1 : 0.4);
      const at = c => [c.x * t.k + t.x, c.y * t.k + t.y, c.r * t.k];
      // disc fills and strokes circle c at its opacities, when it is on the stage.
      const disc = (c, fill, line, fillAlpha, lineAlpha, dashed) => {
        const [x, y, r] = at(c);
        if (x + r < 0 || x - r > v.w || y + r < 0 || y - r > v.h) {
          return;
        }
        ctx.beginPath();
        ctx.arc(x, y, r, 0, 2 * Math.PI);
        Object.assign(ctx, {globalAlpha: fillAlpha, fillStyle: fill});
        ctx.fill();
        Object.assign(ctx, {globalAlpha: lineAlpha, lineWidth: 1, strokeStyle: line});
        ctx.setLineDash(dashed ? [4, 3] : []);
        ctx.stroke();
        ctx.setLineDash([]);
      };
      m.areas.forEach((a, i) => disc(a.zoom, theme.disc, theme.line, shade(i), shade(i)));
      disc(m.disc, theme.disc, theme.text3, shade(-2), shade(-2), true);
      m.interests.forEach((it, i) => {
        const colour = theme.area[m.slots[m.flat ? i : it.area]] || theme.neutral;
        disc(it.zoom, colour, colour, 0.12 * shade(it.area), 0.55 * shade(it.area));
      });
      const r = Math.max(m.dotRadius * t.k, minScaledDotPx);
      const xy = d => [m.docs.zx[d] * t.k + t.x, m.docs.zy[d] * t.k + t.y];
      const areaOf = d => (m.docs.interest[d] >= 0 ? m.docs.area[d] : -2);
      documentPasses(ctx, v, m, xy, r, d => shade(areaOf(d)), focus === -1 ? [1] : [0.4, 1]);
      const emphasis = (s, colour, w) => s && ring(ctx, ...(s.kind === 'document' ? [...xy(s.i), r + 2.5] :
        at(s.kind === 'unsorted' ? m.disc : groupOf(m, s).zoom)), colour, w);
      emphasis(v.hover, theme.text3, 1.5);
      emphasis(sel, theme.accent, 2.2);
      emphasis(sel && sel.kind === 'document' && parentOf(m, sel), theme.accent, 1.2);
      zoomLabels(v, ctx, m, r, focus);
    },
    // pick is what a click means at this zoom: a closed area; an interest in an open one; a document in an open
    // interest, or once the dots are big; a label's group.
    pick(v, x, y) {
      const m = state.model;
      const [wx, wy] = v.t.invert([x, y]);
      const inside = c => (wx - c.x) ** 2 + (wy - c.y) ** 2 <= c.r * c.r;
      const open = c => c.r * v.t.k >= openShare * Math.min(v.w, v.h) || m.dotRadius * v.t.k >= pickDotPx;
      const area = m.flat ? -1 : m.areas.findIndex(a => inside(a.zoom));
      const unsorted = area < 0 && inside(m.disc);
      const interest = (m.flat ? m.interestsBySize : m.areaInterests[area] || []).find(i =>
        inside(m.interests[i].zoom)) ?? -1;
      const d = m.zoomTree.find(wx, wy, Math.max(m.dotRadius * 1.3, 4 / v.t.k));
      const doc = (interest >= 0 || unsorted) && d !== undefined && m.docs.interest[d] === interest ?
        {kind: 'document', i: d} : null;
      if (labelAt(v, x, y) || unsorted) {
        return labelAt(v, x, y) || (open(m.disc) && doc) || {kind: 'unsorted'};
      }
      if (area >= 0 && (!open(m.areas[area].zoom) || interest < 0)) {
        return {kind: 'area', i: area};
      }
      return interest < 0 ? null : (open(m.interests[interest].zoom) && doc) || {kind: 'interest', i: interest};
    },
    // click selects what it picks, flying to a group not already selected; empty space goes up.
    click(v, hit) {
      choose(hit || parentOf(state.model, state.sel), {fly: !hit || hit.kind !== 'document' && !same(hit, state.sel)});
    },
    frame(v, sel) {
      const m = state.model;
      if (sel && sel.kind === 'document') {
        const i = m.docs.interest[sel.i];
        const k = fitTransform(v, around(i >= 0 ? m.interests[i].zoom : m.disc), flightPad).k;
        return centreOn(v, m.docs.zx[sel.i], m.docs.zy[sel.i], Math.max(k, documentDotPx / m.dotRadius));
      }
      return !sel ? fitAll(v) : fitTransform(v, around(sel.kind === 'unsorted' ? m.disc : groupOf(m, sel).zoom),
        sel.kind === 'interest' ? flightPad : discPad);
    },
    legend: m => [words('span', '', 'Circles are interests, sized by their documents' + (m.flat ? '' :
      ', in their areas\' discs')), key('map-key-disc', 'Unsorted')],
    about: m => (m.flat ? 'Each interest is a circle' : 'Each area is a disc holding its interests\' circles') +
      ', sized by its documents, each a dot inside it, with Unsorted the dashed disc beside them.',
  };

  // zoomLabels names what Zoom in shows at this zoom, the selection first: closed areas beside their discs,
  // Unsorted's disc, interests in open areas (every one, in the flat shape), and documents once their dots are big.
  function zoomLabels(v, ctx, m, r, focus) {
    const {t, w, h} = v;
    const [sx, sy, sr] = [x => x * t.k + t.x, y => y * t.k + t.y, c => c.r * t.k];
    const on = c => sx(c.x) + sr(c) > 0 && sx(c.x) - sr(c) < w && sy(c.y) + sr(c) > 0 && sy(c.y) - sr(c) < h;
    const closed = c => sr(c) <= openShare * Math.min(w, h);
    const colourOf = a => (focus >= 0 && a !== focus ? theme.text3 : theme.text);
    const areaFont = fontOf(650, phone.matches ? 11.5 : 12.5);
    const titled = m.dotRadius * t.k >= titlesDotPx;
    const pending = [];
    const place = {
      area(i) {
        const c = m.areas[i].zoom;
        const lines = on(c) && closed(c) ? wrap(groupName(m, 'area', i), areaFont, Math.max(Math.min(2.2 * sr(c), 210),
          120), maxLabelLines) : [];
        [sy(c.y) - sr(c) - 4 - lines.length * 15, sy(c.y) + sr(c) + 4].some(y => lines.length &&
          label(v, pending, lines, sx(c.x), y, areaFont, colourOf(i), 15, {kind: 'area', i}));
      },
      unsorted: () => on(m.disc) && closed(m.disc) && label(v, pending, ['Unsorted · ' + num(m.unsortedDocs.length)],
        sx(m.disc.x), sy(m.disc.y) - sr(m.disc) - 19, areaFont, theme.text2, 15, {kind: 'unsorted'}),
      interest(i) {
        const [c, area] = [m.interests[i].zoom, m.interests[i].area];
        if (sr(c) < nameCirclePx || !on(c) || area >= 0 && closed(m.areas[area].zoom)) {
          return;
        }
        const px = sr(c) > 70 ? 13 : 11.5;
        const font = fontOf(600, px);
        const lines = wrap(groupName(m, 'interest', i), font, Math.max(1.7 * sr(c), titled ? 160 : 60),
          titled ? titledLines : maxLabelLines);
        label(v, pending, lines, sx(c.x), titled ? sy(c.y) - sr(c) - 4 - lines.length * px * 1.2 :
          sy(c.y) - lines.length * px * 0.6, font, colourOf(area), px * 1.2, {kind: 'interest', i});
      },
    };
    const groups = m.areas.map((_, i) => ({kind: 'area', i})).concat({kind: 'unsorted'},
      m.interestsBySize.map(i => ({kind: 'interest', i})));
    const sel = state.sel && state.sel.kind !== 'document' ? state.sel : null;
    for (const g of sel ? [sel].concat(groups.filter(g => !same(g, sel))) : groups) {
      place[g.kind](g.i);
    }
    if (titled) {
      const shown = m.interests.flatMap((it, i) => (on(it.zoom) ? m.interestDocs[i] : []))
        .concat(on(m.disc) ? m.unsortedDocs : []);
      titles(v, pending, m, shown, d => [sx(m.docs.zx[d]) + r + 4, sy(m.docs.zy[d]) - 7],
        Math.min(220, Math.max(90, r * 14)));
    }
    writeLabels(ctx, pending);
  }

  // ------------------------------------------------------------------ panel
  // renderPanel shows the selection: its kind and name in the head (a phone's closed sheet), the rest below.
  function renderPanel() {
    const m = state.model;
    const sel = state.sel;
    kicker.replaceChildren(...[sel && swatch(slotOf(m, sel)), plain(sel ? kindNames[sel.kind] : 'Library')]
      .filter(Boolean));
    panelTitle.replaceChildren(sel ? named('span', isUnlabeled(m, sel) ? 'is-unlabeled' : '', nameOf(m, sel)) :
      plain(plural(m.n, 'document', 'documents')));
    clearButton.hidden = !sel;
    const body = !sel ? libraryPanel(m) : sel.kind === 'area' ? areaPanel(m, sel.i) : sel.kind === 'interest' ?
      interestPanel(m, sel.i) : sel.kind === 'unsorted' ? unsortedPanel(m) : documentPanel(m, sel.i);
    panelBody.replaceChildren(...body.filter(Boolean));
  }

  // stats is a line of counts: each part a number and what it counts, the falsy left out.
  const stats = (...parts) => el('p', {class: 'map-stats'}, ...parts.filter(Boolean).map(([n, what]) =>
    el('span', {}, words('b', '', n), plain(' ' + what))));
  const section = (title, ...kids) => el('section', {class: 'map-section'}, words('h3', '', title), ...kids);
  const rows = items => el('ul', {class: 'map-rows', role: 'list'}, ...items.map(item => el('li', {}, item)));
  const newIn = (m, list) => list.filter(d => m.docs.fit[d] === 'new').length;
  const documentRows = (m, list) => rows(list.map(d => documentRow(m, d, two(m.docs.similarity[d]))));

  // groupRow leads to an area, an interest or Unsorted: its colour, its name on one line, n, and extra.
  function groupRow(m, kind, i, n, extra) {
    const sel = kind === 'unsorted' ? {kind} : {kind, i};
    return go(el('button', {type: 'button', class: 'map-row'}, swatch(slotOf(m, sel)), el('span',
      {class: 'map-row-name'}, named('span', isUnlabeled(m, sel) ? 'is-unlabeled' : '', nameOf(m, sel)), extra),
    words('span', 'n', n)), sel);
  }

  // documentRow leads to document d: its title and host, each on one line, and a number; a new one tagged.
  function documentRow(m, d, n) {
    return go(el('button', {type: 'button', class: 'map-row map-row-doc'}, el('span', {class: 'map-row-name'},
      named('span', '', m.docs.title[d]), named('span', 'map-row-sub', m.docs.host[d])), el('span', {class: 'n'},
      m.docs.fit[d] === 'new' && words('span', 'map-new', 'new'), plain(n))), {kind: 'document', i: d});
  }

  // groupButton is a group's name, leading to it.
  const groupButton = (m, kind, i) => go(el('button', {type: 'button', class: 'map-link-button'}, named('span',
    isUnlabeled(m, {kind, i}) ? 'is-unlabeled' : '', groupName(m, kind, i))), {kind, i});

  function libraryPanel(m) {
    return [
      stats([num(m.n), 'documents'], !m.flat && [num(m.areas.length), 'areas'], [num(m.interests.length),
        'interests'], [num(m.unsortedDocs.length), 'unsorted'], m.newDocs.length && [num(m.newDocs.length), 'new']),
      words('p', 'map-about', (state.view === 'zoom' ? zoomView : allView).about(m)),
      section(m.flat ? 'Interests' : 'Areas', rows(m.top.map((g, i) => groupRow(m, m.flat ? 'interest' : 'area', i,
        num(g.size))).concat(groupRow(m, 'unsorted', -1, num(m.unsortedDocs.length))))),
    ];
  }

  function areaPanel(m, a) {
    const area = m.areas[a];
    const fresh = newIn(m, m.areaDocs[a]);
    return [
      stats([num(area.size), 'documents'], [num(m.areaInterests[a].length), 'interests'], fresh && [num(fresh), 'new'],
        [two(area.cohesion), 'cohesion']),
      section('Interests', rows(m.areaInterests[a].map(i => groupRow(m, 'interest', i, num(m.interests[i].size))))),
      link(interestPage, area.id, 'Open area page', 'btn btn-sm map-open'),
    ];
  }

  function interestPanel(m, i) {
    const it = m.interests[i];
    const list = m.interestDocs[i];
    const [loose, fresh] = [list.filter(d => m.docs.fit[d] === 'loose').length, newIn(m, list)];
    return [
      stats([num(it.size), 'documents'], loose && [num(loose), loose === 1 ? 'loose fit' : 'loose fits'],
        fresh && [num(fresh), 'new'], [two(it.cohesion), 'cohesion']),
      it.area >= 0 && el('dl', {class: 'facts'}, words('dt', '', 'Area'), el('dd', {}, groupButton(m, 'area',
        it.area))),
      section('Closest documents', documentRows(m, list.slice(0, closestShown))),
      it.similar.length && section('Most similar interests', rows(it.similar.map(s => {
        const area = m.interests[s.interest].area;
        return groupRow(m, 'interest', s.interest, two(s.cosine), area >= 0 && area !== it.area &&
          named('span', 'map-elsewhere', '· ' + groupName(m, 'area', area)));
      }))),
      link(interestPage, it.id, 'Open interest page', 'btn btn-sm map-open'),
    ];
  }

  function unsortedPanel(m) {
    const near = new Map();
    for (const i of m.unsortedDocs.map(d => m.docs.nearest[d]).filter(i => i >= 0)) {
      near.set(i, (near.get(i) || 0) + 1);
    }
    const nearest = [...near.entries()].sort((a, b) => b[1] - a[1]).slice(0, nearestShown);
    const fresh = newIn(m, m.unsortedDocs);
    return [
      stats([num(m.unsortedDocs.length), 'documents'], fresh && [num(fresh), 'new']),
      words('p', 'map-about', 'Documents close to no interest yet. ' + (state.view === 'zoom' ?
        'They share the dashed disc beside the map, each on the side of its nearest interest.' :
        'Each sits among the documents most like it, too far from any interest to count as a fit.')),
      nearest.length && section('Nearest interests', rows(nearest.map(([i, n]) => groupRow(m, 'interest', i,
        num(n))))),
      m.unsortedDocs.length && section('Closest to an interest', documentRows(m, m.unsortedDocs.slice(0,
        unsortedShown))),
      link(unsortedPage, '', 'Open Unsorted', 'btn btn-sm map-open'),
    ];
  }

  const fitNames = {member: 'Member', loose: 'Loose fit', unsorted: 'Unsorted', new: 'New since the last rebuild'};

  function documentPanel(m, d) {
    const docs = m.docs;
    const [i, near] = [docs.interest[d], docs.nearest[d]];
    const group = i >= 0 ? i : near;
    const none = text => words('span', 'muted', text);
    const siblings = group >= 0 ? m.interestDocs[group].filter(x => x !== d).slice(0, siblingsShown) : [];
    return [
      el('dl', {class: 'facts'}, words('dt', '', 'Site'), el('dd', {}, named('span', '', docs.host[d] || '—')),
        words('dt', '', i >= 0 ? 'Interest' : 'Nearest'), el('dd', {}, group >= 0 ?
          groupButton(m, 'interest', group) : none('none')),
        !m.flat && words('dt', '', 'Area'), !m.flat && el('dd', {}, docs.area[d] >= 0 ?
          groupButton(m, 'area', docs.area[d]) : none(i >= 0 ? 'none' : 'Unsorted')),
        words('dt', '', 'Fit'), el('dd', {}, words('span', 'map-fit', fitNames[docs.fit[d]]), none(' · similarity ' +
          two(docs.similarity[d]) + (i >= 0 ? ' to its interest' : near >= 0 ? ' to its nearest interest' : '')))),
      link(documentPage, docs.id[d], 'Open document', 'btn btn-primary btn-sm map-open'),
      siblings.length && section(i >= 0 ? 'Also in this interest' : 'In its nearest interest',
        documentRows(m, siblings)),
    ];
  }

  // renderCrumbs is the path to the selection: the library, its area, its interest (Unsorted, for an unsorted
  // document) and the document; each ancestor leads to itself, and the last is where the map is.
  function renderCrumbs() {
    const m = state.model;
    const path = [null];
    for (let s = state.sel; s; s = parentOf(m, s)) {
      path.splice(1, 0, s);
    }
    crumbs.replaceChildren(...path.flatMap((s, x) => {
      const last = x === path.length - 1;
      const item = named(last ? 'span' : 'button', last ? 'map-crumb-here' : 'map-crumb', nameOf(m, s));
      item.setAttribute(last ? 'aria-current' : 'type', last ? 'location' : 'button');
      return (x ? [words('span', 'map-crumb-sep', '›')] : []).concat(last ? item : go(item, s));
    }));
  }

  // ------------------------------------------------------------------ search
  const found = {hits: [], cursor: -1};

  // runSearch ranks what matches the query: a prefix, then a word's start, then anywhere, then every word;
  // areas before interests before documents, shorter names first; maxHits at most, so many of each kind.
  function runSearch() {
    const q = search.value.trim().toLowerCase();
    const terms = q.split(/\s+/);
    const ranked = [];
    for (const entry of q.length >= minQuery ? state.model.index : []) {
      const score = entry.text.startsWith(q) ? 0 : entry.text.includes(' ' + q) ? 1 : entry.text.includes(q) ? 2 :
        terms.length > 1 && terms.every(term => entry.text.includes(term)) ? 3 : -1;
      if (score >= 0) {
        ranked.push([score, kindRank[entry.sel.kind], entry.name.length, entry]);
      }
    }
    ranked.sort((a, b) => a[0] - b[0] || a[1] - b[1] || a[2] - b[2]);
    const taken = {area: 0, interest: 0, document: 0};
    found.hits = ranked.map(r => r[3]).filter(e => taken[e.sel.kind]++ < hitCaps[e.sel.kind]).slice(0, maxHits);
    found.cursor = found.hits.length ? 0 : -1;
    renderHits(q.length >= minQuery);
  }

  // renderHits lists the matches, the cursor's selected, or closes the list.
  function renderHits(open) {
    const items = found.hits.map((hit, x) => go(el('li', {id: 'map-hit-' + x, role: 'option',
      'aria-selected': String(x === found.cursor)}, words('span', 'kind', kindNames[hit.sel.kind]),
    named('span', 'name', hit.name), named('span', 'meta', hit.meta)), hit.sel));
    hitList.replaceChildren(...(!open ? [] : items.length ? items : [el('li', {class: 'none', role: 'option',
      'aria-disabled': 'true'}, plain('Nothing matches'))]));
    search.setAttribute('aria-expanded', String(open));
    if (open && found.cursor >= 0) {
      search.setAttribute('aria-activedescendant', 'map-hit-' + found.cursor);
    } else {
      search.removeAttribute('aria-activedescendant');
    }
  }

  function pickHit(sel) {
    renderHits(false);
    choose(sel, {fly: true});
  }

  // searchKeys: arrows move, Enter picks, Escape closes the list, then clears the query, then leaves the box.
  function searchKeys(e) {
    const n = found.hits.length;
    if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && n) {
      found.cursor = (found.cursor + (e.key === 'ArrowDown' ? 1 : -1) + n) % n;
      renderHits(true);
      hitList.children[found.cursor].scrollIntoView({block: 'nearest'});
    } else if (e.key === 'Enter' && found.cursor >= 0 && hitList.childElementCount) {
      pickHit(found.hits[found.cursor].sel);
    } else if (e.key === 'Escape' && hitList.childElementCount) {
      renderHits(false);
    } else if (e.key === 'Escape' && search.value) {
      search.value = '';
    } else if (e.key === 'Escape') {
      search.blur();
    } else {
      return;
    }
    e.preventDefault();
  }

  // ------------------------------------------------------------------ controller
  // choose selects sel in both views: the panel, the breadcrumb and the address follow, a phone's sheet
  // opens, the view flies to it when how.fly asks (at once with how.instant), and a group stands out on All
  // documents. how.focus moves the keyboard to the panel's heading, for a choice in the panel or breadcrumb.
  function choose(sel, how) {
    state.sel = sel;
    state.highlight = highlighted(state.model, sel);
    renderPanel();
    renderCrumbs();
    say();
    if (phone.matches && sel) {
      setSheet(true);
    }
    syncAddress();
    const v = views[state.view];
    if (how.fly) {
      land(v, sel, how.instant ? 0 : flightMs);
    }
    request(v);
    if (how.focus) {
      panelTitle.focus();
    }
  }

  // syncAddress keeps the address on the view and the selection as mapHref writes them, never adding to the history,
  // best effort: Safari and Firefox throw past a rate limit on replaceState, and that must never stop the map.
  function syncAddress() {
    const params = {select: selectionParam(state.model, state.sel), view: state.view === 'all' ? '' : state.view};
    const query = new URLSearchParams(Object.entries(params).filter(([, value]) => value)).toString();
    try {
      history.replaceState(history.state, '', query ? mapPage + '?' + query : mapPage);
    } catch { /* throttled: the address catches up at the next change */ }
  }

  // activate shows view name, its tab, canvas and legend, flown to the selection or fitted to the whole map. The
  // panel is rendered first: a phone's sheet, which fits keep clear of, is only as tall as what it holds.
  function activate(name, focusTab) {
    if (name === state.view) {
      return;
    }
    state.view = name;
    for (const [n, tab] of Object.entries(tabs)) {
      tab.setAttribute('aria-selected', String(n === name));
      tab.tabIndex = n === name ? 0 : -1;
      canvases[n].hidden = n !== name;
    }
    stage.setAttribute('aria-labelledby', tabs[name].id);
    if (focusTab) {
      tabs[name].focus();
    }
    const v = views[name];
    hoverAt(v);
    legend.replaceChildren(...(name === 'zoom' ? zoomView : allView).legend(state.model));
    renderPanel();
    resize(v);
    land(v, state.sel, flightMs);
    syncAddress();
  }

  // setSheet opens or closes a phone's sheet; open, it brings the stage up to the header, so that the map it
  // leaves showing is as tall as it can be.
  function setSheet(open) {
    panel.classList.toggle('is-open', open);
    sheetToggle.setAttribute('aria-expanded', String(open));
    sheetToggle.setAttribute('aria-label', open ? 'Hide the details' : 'Show the details');
    if (open && phone.matches) {
      stage.scrollIntoView({block: 'start'});
    }
  }

  // goUp closes a phone's open sheet, or else selects the selection's parent.
  function goUp() {
    if (phone.matches && panel.classList.contains('is-open')) {
      setSheet(false);
    } else if (state.sel) {
      choose(parentOf(state.model, state.sel), {fly: true});
    }
  }

  const editable = node => node instanceof HTMLElement && (node.isContentEditable ||
    ['INPUT', 'TEXTAREA', 'SELECT'].includes(node.tagName));

  // pageKeys: / searches, Escape goes up, 0 fits, + and - zoom; none with a modifier or while typing.
  function pageKeys(e) {
    const v = views[state.view];
    const act = !e.metaKey && !e.ctrlKey && !e.altKey && !editable(e.target) && {'/': () => search.focus(),
      Escape: goUp, 0: () => land(v, null, flightMs), '+': () => zoomBy(v, zoomStep), '=': () => zoomBy(v, zoomStep),
      '-': () => zoomBy(v, 1 / zoomStep)}[e.key];
    if (act) {
      e.preventDefault();
      act();
    }
  }

  // tabKeys move between the tabs, as the ARIA tabs pattern does: arrows, Home and End.
  function tabKeys(e) {
    const to = {ArrowRight: 'other', ArrowLeft: 'other', Home: 'all', End: 'zoom'}[e.key];
    if (to) {
      e.preventDefault();
      activate(to === 'other' ? (state.view === 'all' ? 'zoom' : 'all') : to, true);
    }
  }

  // onTarget is a click handler calling fn with where the control of selector under the click leads, if anywhere.
  const onTarget = (selector, fn) => e => {
    const control = e.target.closest(selector);
    if (targets.has(control)) {
      fn(targets.get(control));
    }
  };

  // chosen selects what a control of the panel or the breadcrumb leads to, moving the keyboard to the panel.
  const chosen = onTarget('button', sel => choose(sel, {fly: true, focus: true}));

  // wire connects the controls, once: no listener or observer is made per selection or per frame.
  function wire() {
    Object.values(views).forEach(v => new ResizeObserver(guarded(() => v.name === state.view && resize(v)))
      .observe(v.canvas));
    Object.entries(tabs).forEach(([name, tab]) => listen(tab, 'click', () => activate(name, false)));
    listen(tabs.all.parentElement, 'keydown', tabKeys);
    listen(panelBody, 'click', chosen);
    listen(crumbs, 'click', chosen);
    listen($('map-zoom-in'), 'click', () => zoomBy(views[state.view], zoomStep));
    listen($('map-zoom-out'), 'click', () => zoomBy(views[state.view], 1 / zoomStep));
    listen($('map-zoom-fit'), 'click', () => land(views[state.view], null, flightMs));
    listen(clearButton, 'click', () => choose(null, {fly: true, focus: true}));
    listen(sheetToggle, 'click', () => setSheet(!panel.classList.contains('is-open')));
    listen(search, 'input', runSearch);
    listen(search, 'focus', () => search.value.trim().length >= minQuery && runSearch());
    listen(search, 'blur', () => renderHits(false));
    listen(search, 'keydown', searchKeys);
    listen(hitList, 'mousedown', e => e.preventDefault());
    listen(hitList, 'click', onTarget('li', pickHit));
    listen(document, 'keydown', pageKeys);
    const retheme = () => {
      readTheme();
      request(views[state.view]);
    };
    listen(matchMedia('(prefers-color-scheme: dark)'), 'change', retheme);
    new MutationObserver(guarded(retheme)).observe(document.documentElement, {attributes: true,
      attributeFilter: ['data-theme']});
    listen(phone, 'change', () => setSheet(false));
    watchPixelRatio();
  }

  // watchPixelRatio redraws at a new pixel ratio (another screen, say): one listener at a time.
  function watchPixelRatio() {
    listen(matchMedia('(resolution: ' + devicePixelRatio + 'dppx)'), 'change', () => {
      resize(views[state.view]);
      watchPixelRatio();
    }, {once: true});
  }

  // ------------------------------------------------------------------ start
  // setUp builds the model and the views, draws the view the address asked for, and shows the selection it
  // asked for at once, without a flight; one not on the map leaves the library shown, with a note.
  function setUp(data) {
    readTheme();
    state.model = buildModel(data);
    views.all = makeView('all', allView);
    views.zoom = makeView('zoom', zoomView);
    wire();
    const first = state.view;
    state.view = '';
    activate(first, false);
    const asked = root.dataset.select || '';
    const sel = asked ? resolve(state.model, asked) : null;
    choose(sel || null, {fly: !!sel, instant: true});
    if (asked && !sel) {
      missing(asked);
    }
  }

  // missing says what the address asked for isn't on the map: a group a rebuild retired, or a document.
  function missing(asked) {
    const [kind, id] = kindAndID(asked);
    if (kind === 'document') {
      say(words('span', '', 'That document isn\'t on the map: it may have failed since the rebuild, or not been ' +
        'placed yet.'));
    } else {
      say(words('span', '', 'That ' + kind + ' isn\'t on the map: a rebuild may have retired it. '),
        link(interestPage, id, 'Its page'), words('span', '', ' says what became of it.'));
    }
  }

  // start reads the map and draws it, or says why it can't: the one place a read starts, Try again's too.
  async function start() {
    say(words('span', '', 'Loading the map…'));
    const result = await load();
    if (result.kind !== 'ok') {
      showFailure(result);
      return;
    }
    say();
    guarded(setUp)(result.data);
  }

  if (typeof d3 !== 'object' || !d3.zoom || !d3.quadtree || !d3.transition) {
    say(words('span', '', 'The map\'s scripts didn\'t load: reload the page.'));
    return;
  }
  listen(status, 'click', e => e.target.closest('button') && start());
  start();
})();
