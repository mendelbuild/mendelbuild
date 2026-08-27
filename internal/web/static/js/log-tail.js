// Tails a log feed into a panel rendered by the "log-tail" partial, so a
// running generation or deploy streams new lines instead of reloading the
// whole page every few seconds.
//
// A panel opts in with data attributes:
//   data-log-feed   URL returning {status, logs:[{logged_at, level, message}]}
//   data-log-status the owning object's status at render time
//   data-log-live   "true" while the underlying work is still running
//
// The server renders the lines that exist at page load; this only appends what
// arrives afterwards. When the feed reports a status different from the one the
// page was rendered with, the rest of the page is stale, so we reload once.
(function () {
  'use strict';

  var POLL_MS = 2000;
  var BACKOFF_MS = 15000; // after repeated failures, back off rather than hammer

  function pad(n) { return n < 10 ? '0' + n : '' + n; }

  // Matches the server's LogTimeFormat ("2006/01/02 15:04:05") so appended
  // lines are indistinguishable from server-rendered ones.
  function formatTimestamp(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.getFullYear() + '/' + pad(d.getMonth() + 1) + '/' + pad(d.getDate()) +
      ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
  }

  function levelCell(level) {
    var td = document.createElement('td');
    td.style.paddingRight = '8px';
    td.style.verticalAlign = 'top';
    var span = document.createElement('span');
    if (level === 'milestone') {
      span.style.color = '#28a745';
      span.style.fontWeight = 'bold';
      span.textContent = '[MILESTONE]';
    } else if (level === 'error') {
      span.style.color = '#dc3545';
      span.style.fontWeight = 'bold';
      span.textContent = '[ERROR]';
    } else if (level === 'info') {
      span.style.color = '#007bff';
      span.textContent = '[INFO]';
    } else {
      span.style.color = '#6c757d';
      span.textContent = '[' + level + ']';
    }
    td.appendChild(span);
    return td;
  }

  function buildRow(line) {
    var tr = document.createElement('tr');

    var ts = document.createElement('td');
    ts.style.whiteSpace = 'nowrap';
    ts.style.color = '#999';
    ts.style.paddingRight = '10px';
    ts.style.verticalAlign = 'top';
    ts.textContent = formatTimestamp(line.logged_at);
    tr.appendChild(ts);

    tr.appendChild(levelCell(line.level));

    var msg = document.createElement('td');
    msg.style.wordBreak = 'break-word';
    // textContent, not innerHTML: log messages carry tool output and error
    // text, which must never be interpreted as markup.
    msg.textContent = line.message;
    tr.appendChild(msg);

    return tr;
  }

  function Tailer(panel) {
    this.panel = panel;
    this.feedURL = panel.getAttribute('data-log-feed');
    this.status = panel.getAttribute('data-log-status') || '';
    this.scroll = panel.querySelector('[data-log-scroll]');
    this.body = panel.querySelector('[data-log-body]');
    this.empty = panel.querySelector('[data-log-empty]');
    // The server rendered this many lines; everything past it is new.
    this.rendered = this.body ? this.body.querySelectorAll('tr').length : 0;
    this.failures = 0;
    this.timer = null;
  }

  Tailer.prototype.atBottom = function () {
    if (!this.scroll) return true;
    // Within a few pixels counts as pinned, so rounding does not unstick it.
    return this.scroll.scrollHeight - this.scroll.scrollTop - this.scroll.clientHeight < 4;
  };

  Tailer.prototype.append = function (lines) {
    if (!this.body || !lines.length) return;
    // Only auto-scroll if the reader was already at the bottom; otherwise they
    // have scrolled up to read something and must not be yanked away.
    var pinned = this.atBottom();

    var frag = document.createDocumentFragment();
    for (var i = 0; i < lines.length; i++) frag.appendChild(buildRow(lines[i]));
    this.body.appendChild(frag);

    if (this.empty) {
      this.empty.style.display = 'none';
    }
    if (pinned && this.scroll) {
      this.scroll.scrollTop = this.scroll.scrollHeight;
    }
  };

  Tailer.prototype.poll = function () {
    var self = this;
    fetch(this.feedURL, { headers: { 'Accept': 'application/json' }, credentials: 'same-origin' })
      .then(function (res) {
        if (!res.ok) throw new Error('feed responded ' + res.status);
        return res.json();
      })
      .then(function (feed) {
        self.failures = 0;

        var logs = feed.logs || [];
        if (logs.length > self.rendered) {
          self.append(logs.slice(self.rendered));
          self.rendered = logs.length;
        }

        // The status drives buttons, badges and URLs elsewhere on the page.
        // Once it moves, only a reload can bring those up to date.
        if (feed.status && feed.status !== self.status) {
          self.stop();
          window.location.reload();
          return;
        }
        self.schedule(POLL_MS);
      })
      .catch(function () {
        self.failures++;
        // Transient failures are expected across a deploy; keep trying, slower.
        self.schedule(self.failures > 3 ? BACKOFF_MS : POLL_MS);
      });
  };

  Tailer.prototype.schedule = function (delay) {
    var self = this;
    this.timer = window.setTimeout(function () { self.poll(); }, delay);
  };

  Tailer.prototype.stop = function () {
    if (this.timer) window.clearTimeout(this.timer);
    this.timer = null;
  };

  Tailer.prototype.start = function () {
    if (!this.feedURL || !this.body) return;
    this.schedule(POLL_MS);
  };

  function init() {
    // Every panel starts scrolled to its newest line.
    document.querySelectorAll('[data-log-scroll]').forEach(function (el) {
      el.scrollTop = el.scrollHeight;
    });

    document.querySelectorAll('[data-log-feed][data-log-live="true"]').forEach(function (panel) {
      new Tailer(panel).start();
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
