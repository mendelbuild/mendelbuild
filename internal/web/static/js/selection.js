// The variation comparison table: grades arrive after the page does, and the
// winner cannot be chosen until one is picked.
//
// This used to be two near-identical copies of the same script inside the
// template, one per branch of an {{if}}, each building grade badges out of
// hardcoded hex values. The colours now come from the badge classes in
// components.css, so a grade is coloured by the same six tones as everything
// else in the app.
(function () {
  'use strict';

  // Score bands, worst to best. Mirrors the tone vocabulary: a poor score is a
  // failure, a middling one is a request for attention, a good one is success.
  function toneForScore(score) {
    if (score >= 0.8) return 'success';
    if (score >= 0.6) return 'progress';
    if (score >= 0.4) return 'waiting';
    return 'failure';
  }

  function startDemoInNewTab(projectID, variationID) {
    fetch('/p/' + projectID + '/variations/' + variationID + '/start-demo', { method: 'POST' })
      .then(function () {
        window.open('/p/' + projectID + '/variations/' + variationID, '_blank');
      })
      .catch(function (err) {
        console.error('Failed to start demo:', err);
        alert('Could not start the demo. The variation page has the details.');
      });
  }
  window.startDemoInNewTab = startDemoInNewTab;

  function armWinnerSelection() {
    const button = document.getElementById('select-button');
    if (!button) return;
    // Picking a winner merges code to main, so the button stays inert until a
    // choice exists rather than submitting an empty selection.
    document.querySelectorAll('.winner-radio').forEach(function (radio) {
      radio.addEventListener('change', function () {
        if (!document.querySelector('.winner-radio:checked')) return;
        button.disabled = false;
        button.textContent = 'Merge this variation';
      });
    });
  }

  function watchHorizontalScroll() {
    const container = document.querySelector('[data-compare-scroll]');
    const indicator = document.querySelector('[data-compare-more]');
    if (!container || !indicator) return;

    function check() {
      const overflows = container.scrollWidth > container.clientWidth;
      const atEnd = container.scrollLeft + container.clientWidth >= container.scrollWidth - 5;
      indicator.hidden = !(overflows && !atEnd);
    }
    container.addEventListener('scroll', check);
    window.addEventListener('resize', check);
    check();
  }

  function loadGrades() {
    const root = document.querySelector('[data-evaluate-url]');
    if (!root || document.querySelectorAll('.grade-cell').length === 0) return;

    fetch(root.dataset.evaluateUrl)
      .then(function (response) { return response.json(); })
      .then(function (data) {
        (data.evaluations || []).forEach(function (item) {
          (item.scores || []).forEach(function (score) {
            const cell = document.querySelector(
              '.grade-cell[data-variation-id="' + item.variation_id +
              '"][data-criterion="' + score.criterion_name + '"]');
            if (!cell) return;

            const badge = document.createElement('span');
            badge.className = 'badge badge-' + toneForScore(score.score);
            badge.textContent = score.score.toFixed(1);
            if (score.rationale) badge.title = score.rationale;
            cell.replaceChildren(badge);
          });
        });
        clearPending('—');
      })
      .catch(function (err) {
        console.error('Failed to load grades:', err);
        // Say the grades are missing rather than leaving spinners turning
        // forever, which reads as "still working" and never resolves.
        clearPending('?');
      });
  }

  function clearPending(text) {
    document.querySelectorAll('.grade-loading').forEach(function (el) {
      const dash = document.createElement('span');
      dash.className = 'subtle';
      dash.textContent = text;
      el.replaceWith(dash);
    });
  }

  function init() {
    armWinnerSelection();
    watchHorizontalScroll();
    loadGrades();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
