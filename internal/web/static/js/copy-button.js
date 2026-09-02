// Copy the contents of the element a [data-copy] button names.
//
// The button reports what happened in its own label: a copy that silently
// succeeds and one that silently fails look identical, and the user only finds
// out which when they paste.
//
// The source is a hidden element holding the script verbatim, not the syntax
// highlighted block on screen, so what reaches the clipboard is exactly what
// has to run.
document.addEventListener('click', function (event) {
    const button = event.target.closest('[data-copy], [data-copy-adjacent]');
    if (!button) return;

    // Two ways to name what gets copied. An id for a single big block on the
    // page; the enclosing .copyable for values that repeat, where an id per row
    // would have to be invented and kept unique for no benefit.
    let source;
    if (button.hasAttribute('data-copy')) {
        source = document.getElementById(button.getAttribute('data-copy'));
    } else {
        const holder = button.closest('.copyable');
        source = holder && holder.querySelector('.copy-value');
    }
    if (!source) return;

    const label = button.querySelector('.btn-copy-label');
    const done = function (message) {
        // An icon-only button has no label to change, so it reports through the
        // tooltip and a class instead of silently doing nothing visible.
        if (!label) {
            button.classList.add('is-copied');
            button.setAttribute('title', message);
            setTimeout(function () {
                button.classList.remove('is-copied');
                button.setAttribute('title', 'Copy');
            }, 1600);
            return;
        }
        if (!button.dataset.originalLabel) {
            button.dataset.originalLabel = label.textContent;
        }
        label.textContent = message;
        setTimeout(function () {
            label.textContent = button.dataset.originalLabel;
        }, 1600);
    };

    // A final line with no newline is a line the shell has not been told is
    // finished, which is how a paste ends up sitting at a continuation prompt.
    let text = source.textContent;
    if (button.hasAttribute('data-copy') && text && !text.endsWith('\n')) {
        // Scripts only. A trailing newline in a DNS provider's form field is at
        // best ignored and at worst stored.
        text += '\n';
    }

    navigator.clipboard.writeText(text).then(
        function () { done('Copied'); },
        function () { done('Press ⌘C'); }
    );
});
