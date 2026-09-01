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

    navigator.clipboard.writeText(source.textContent).then(
        function () { done('Copied'); },
        function () { done('Press ⌘C'); }
    );
});
