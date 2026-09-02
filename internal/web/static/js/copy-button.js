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
    const button = event.target.closest('[data-copy]');
    if (!button) return;

    const source = document.getElementById(button.getAttribute('data-copy'));
    if (!source) return;

    const label = button.querySelector('.btn-copy-label') || button;
    const done = function (text) {
        if (!button.dataset.originalLabel) {
            button.dataset.originalLabel = label.textContent;
        }
        label.textContent = text;
        setTimeout(function () {
            label.textContent = button.dataset.originalLabel;
        }, 1600);
    };

    // A final line with no newline is a line the shell has not been told is
    // finished, which is how a paste ends up sitting at a continuation prompt.
    let text = source.textContent;
    if (text && !text.endsWith('\n')) text += '\n';

    navigator.clipboard.writeText(text).then(
        function () { done('Copied'); },
        function () { done('Press ⌘C'); }
    );
});
