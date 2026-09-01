// Copy the contents of the element a [data-copy] button names.
//
// The button reports what happened in its own label: a copy that silently
// succeeds and a copy that silently fails look identical, and the user only
// finds out which when they paste.
document.addEventListener('click', function (event) {
    const button = event.target.closest('[data-copy]');
    if (!button) return;

    const source = document.getElementById(button.getAttribute('data-copy'));
    if (!source) return;

    const done = function (label) {
        const original = button.dataset.originalLabel || button.textContent;
        button.dataset.originalLabel = original;
        button.textContent = label;
        setTimeout(function () { button.textContent = original; }, 1600);
    };

    navigator.clipboard.writeText(source.textContent).then(
        function () { done('Copied'); },
        function () { done('Press ⌘C'); }
    );
});
