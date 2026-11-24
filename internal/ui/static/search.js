/* The search page's requests, beside htmx's. Typing searches the box's
   words once it has been still for 400 ms (hx-trigger), and Enter or the
   Search button submits the form, which understands them; the two share
   one hx-sync, so the later request replaces the earlier. Enter pressed
   within the delay would then lose to the search of the very words it
   submitted, still waiting out that delay: the understanding aborted, the
   words' page shown. htmx keeps the delay's timer to itself, and with
   allowEval off no trigger filter can say "unless submitted since", so a
   field's request is dropped here, as htmx confirms it, while its form
   has been submitted since the field last changed: all but ask.js's
   curio:search, sent on leaving Ask, when no understanding is on its way
   (entering Ask aborted it). Typing after Enter searches as before, and
   replaces the understanding. An understood page's follow-up
   (#rerank-status) swaps no error in: the pages swap 4xx and 5xx answers,
   and one there would wipe the results it was sent to reorder. Its answer
   is swapped in silently: #results, a live region, reads out the searches
   typed, Entered and paged to, not the same list reordered, which
   #rerank-said says in one line. htmx gives focus back to the element of
   the same id, but focus in a result the reorder moved off the page falls
   to the body, as does focus on an element htmx keeps (hx-preserve) in a
   browser without moveBefore: it goes back to that element, else to the
   title of the result now in that place, else to the first result's that
   can take it (one in the closed rest of a search by meaning alone
   can't), and only from inside #ranking. It sends nothing. */
(function () {
  'use strict';

  const results = document.getElementById('results');
  let submitted = null; // the form submitted since its fields last changed
  let held = null; // what had focus in #ranking as the follow-up's answer swapped in, and its result's place

  document.addEventListener('input', function (event) {
    if (event.target.form === submitted) {
      submitted = null;
    }
  });
  document.addEventListener('htmx:confirm', function (event) {
    const elt = event.detail.elt;
    const leavingAsk = event.detail.triggeringEvent?.type === 'curio:search';
    if (elt instanceof HTMLFormElement) {
      submitted = elt;
    } else if (submitted !== null && elt.form === submitted && !leavingAsk) {
      event.preventDefault();
    }
  });
  document.addEventListener('htmx:beforeSwap', function (event) {
    const followUp = event.detail.requestConfig.elt.id === 'rerank-status';
    if (followUp && event.detail.isError) {
      event.detail.shouldSwap = false;
    }
    results?.setAttribute('aria-live', followUp ? 'off' : 'polite');
    const ranking = document.getElementById('ranking');
    const focused = document.activeElement;
    held = followUp && ranking?.contains(focused) ?
      {focused, place: Array.from(ranking.querySelectorAll('.result')).indexOf(focused.closest('.result'))} : null;
  });
  // htmx's history cache is off, so each swap's beforeSwap precedes its afterSwap: held is the follow-up's or null.
  document.addEventListener('htmx:afterSwap', function () {
    const titles = document.querySelectorAll('#ranking .result-title a');
    const candidates = held === null ? [] : [held.focused.isConnected ? held.focused : null,
      held.place < 0 ? null : titles[held.place], titles[0]];
    held = null;
    for (const candidate of candidates) {
      if (document.activeElement === document.body) {
        candidate?.focus();
      }
    }
  });
})();
