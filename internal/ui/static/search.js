/* The search page's requests, beside htmx's. Typing searches the box's
   words once it has been still for 400 ms (hx-trigger), and Enter or the
   Search button submits the form, which understands them; the two share
   one hx-sync, so the later request replaces the earlier. Enter pressed
   within the delay would then lose to the search of the very words it
   submitted, still waiting out that delay: the understanding aborted, the
   words' page shown. htmx keeps the delay's timer to itself, and with
   allowEval off no trigger filter can say "unless submitted since", so a
   field's request is dropped here, as htmx confirms it, while its form
   has been submitted since the field last changed. Typing after Enter
   searches as before, and replaces the understanding. It sends nothing. */
(function () {
  'use strict';

  let submitted = null; // the form submitted since its fields last changed

  document.addEventListener('input', function (event) {
    if (event.target.form === submitted) {
      submitted = null;
    }
  });
  document.addEventListener('htmx:confirm', function (event) {
    const elt = event.detail.elt;
    if (elt instanceof HTMLFormElement) {
      submitted = elt;
    } else if (submitted !== null && elt.form === submitted) {
      event.preventDefault();
    }
  });
})();
