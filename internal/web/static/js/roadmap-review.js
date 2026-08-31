// The roadmap review table and its dependency graph.
//
// Reads window.__roadmapReview, which the page sets from server data:
//   existingHops  [{name, status, is_terminal}]  Hops that already exist
//   objectives    {id: description}              For naming what a Hop serves
//   proposedHops  [{name, depends_on}]           The proposal being reviewed
//
// The status badges here used to be built from hardcoded hex values, so a
// rejected Hop in this table was a different red from a rejected Hop anywhere
// else. They now use the same badge classes as the rest of the app.
(function () {
  'use strict';

  const data = window.__roadmapReview || {};
  const existingHops = data.existingHops || [];
  const objectives = data.objectives || {};
  const proposedHops = data.proposedHops || [];

  const existingByName = {};
  existingHops.forEach(function (h) { existingByName[h.name] = h; });

  // What an existing Hop's status means for this review. A Hop already decided
  // is not up for debate; one still running is context; anything absent from
  // the project is what the reader is actually being asked to approve.
  function badgeFor(existing) {
    if (!existing) return { label: 'new', tone: 'success', outline: true };
    if (existing.status === 'rejected') return { label: 'rejected', tone: 'failure' };
    if (existing.status === 'abandoned') return { label: 'abandoned', tone: 'neutral' };
    if (existing.is_terminal) return { label: 'done', tone: 'success' };
    return { label: existing.status, tone: 'progress' };
  }

  function renderBadge(cell, spec) {
    const badge = document.createElement('span');
    badge.className = 'badge badge-' + spec.tone + (spec.outline ? ' badge-outline' : '');
    badge.textContent = spec.label;
    cell.replaceChildren(badge);
  }

  function decorateTable() {
    const tbody = document.getElementById('hops-table-body');
    if (!tbody) return;

    // New Hops first: they are the part of the proposal that is actually being
    // decided, and burying them among already-settled ones hides the ask.
    const rows = Array.from(tbody.querySelectorAll('tr'));
    rows.sort(function (a, b) {
      const aExisting = Boolean(existingByName[a.dataset.hopName]);
      const bExisting = Boolean(existingByName[b.dataset.hopName]);
      if (aExisting === bExisting) return 0;
      return aExisting ? 1 : -1;
    });
    rows.forEach(function (row) { tbody.appendChild(row); });

    rows.forEach(function (row) {
      const existing = existingByName[row.dataset.hopName];
      const statusCell = row.querySelector('.hop-status');
      if (statusCell) renderBadge(statusCell, badgeFor(existing));
      if (existing) row.classList.add('is-existing');

      const objCell = row.querySelector('.hop-objectives');
      if (!objCell) return;
      const ids = (row.dataset.objectiveIds || '').split(',').filter(Boolean);
      const names = ids.map(function (id) { return objectives[id]; }).filter(Boolean);
      if (names.length === 0) {
        const none = document.createElement('span');
        none.className = 'empty';
        none.textContent = 'none';
        objCell.replaceChildren(none);
        return;
      }
      objCell.replaceChildren.apply(objCell, names.map(function (name) {
        const line = document.createElement('div');
        line.className = 'text-sm muted';
        // Full text in the tooltip: truncating without it loses information.
        line.title = name;
        line.textContent = name.length > 40 ? name.slice(0, 38) + '…' : name;
        return line;
      }));
    });
  }

  function drawGraph() {
    const container = document.getElementById('roadmap-dag');
    if (!container || proposedHops.length === 0) return;
    if (typeof RoadmapDAG === 'undefined') {
      container.innerHTML = '<p class="empty">The dependency graph could not be drawn. ' +
        'The table below has the same dependencies.</p>';
      return;
    }

    const nodes = proposedHops.map(function (h) {
      const existing = existingByName[h.name];
      return {
        id: h.name,
        name: h.name,
        status: existing ? existing.status : null,
        isTerminal: existing ? existing.is_terminal : false,
        isNew: !existing,
      };
    });

    const edges = [];
    proposedHops.forEach(function (h) {
      (h.depends_on || []).forEach(function (dep) {
        edges.push({ from: dep, to: h.name });
      });
    });

    RoadmapDAG.renderSimple(container, nodes, edges);
  }

  function init() {
    decorateTable();
    drawGraph();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
