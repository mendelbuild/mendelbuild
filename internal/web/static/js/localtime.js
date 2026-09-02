// Timestamps in the reader's own clock.
//
// Every instant Mendel stores is exactly that: an absolute moment with no zone
// attached (see the timestamp section of CLAUDE.md). Which zone to show it in
// is a presentation question, and the only party that knows the answer is the
// browser. So the server renders the instant in UTC inside a <time> element
// carrying the machine-readable value, and this replaces the text with the same
// moment on the reader's clock.
//
// Without JavaScript you are left with a correct instant in UTC, labelled UTC,
// rather than a wrong one in an unstated zone.
//
// Calendar dates are deliberately NOT handled here. A key result due on 1
// November is due on the 1st wherever you read it; shifting it by a zone would
// show 31 October to anyone west of UTC, which is an off-by-one on the very
// thing the row is about. Those are rendered as plain text by the server.
(function () {
  'use strict';

  var shapes = {
    date: { year: 'numeric', month: 'short', day: 'numeric' },
    datetime: {
      year: 'numeric', month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit',
    },
    // Dense tables, where the year is noise.
    short: { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' },
    // Log lines: fixed width, and seconds matter.
    log: {
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    },
  };

  function format(iso, shape) {
    var when = new Date(iso);
    if (isNaN(when.getTime())) return null;
    try {
      return new Intl.DateTimeFormat(undefined, shapes[shape] || shapes.datetime).format(when);
    } catch (e) {
      // A browser without Intl, or a shape it dislikes. The server's UTC text
      // is still on the page and is still true.
      return null;
    }
  }

  // apply rewrites every <time data-at> under root. It takes a root because log
  // panels append rows long after the page has loaded.
  function apply(root) {
    var scope = root || document;
    scope.querySelectorAll('time[data-at]').forEach(function (el) {
      var text = format(el.getAttribute('datetime'), el.getAttribute('data-at'));
      if (text !== null) el.textContent = text;
    });
  }

  window.MendelTime = { format: format, apply: apply };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { apply(); });
  } else {
    apply();
  }
})();
