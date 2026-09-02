// Complete the setup script from a value the user types, and only allow copying
// once it is filled in.
//
// The script carries a placeholder it cannot fill for itself. Left to the user
// it is an edit made in a terminal, after pasting, at the moment they are least
// able to check it -- and pasted unedited it is a syntax error. Collecting it
// here means what reaches the clipboard runs as it stands.
(function () {
    const panel = document.querySelector('.script-input');
    if (!panel) return;

    const input = panel.querySelector('input');
    const placeholder = panel.getAttribute('data-placeholder');
    const credential = panel.getAttribute('data-credential');
    const copySource = document.getElementById('setup-script');
    const copyButton = document.querySelector('[data-copy="setup-script"]');
    if (!input || !placeholder || !copySource) return;

    // The script as written, kept aside so each edit re-substitutes from the
    // original rather than from the last substitution.
    const template = copySource.textContent;
    const lineTemplates = [...document.querySelectorAll('.script-line')].map(function (el) {
        return {el: el, text: el.textContent};
    });

    function apply() {
        const value = input.value.trim();
        const filled = value.length > 0;
        const replacement = filled ? value : placeholder;

        copySource.textContent = template.split(placeholder).join(replacement);
        lineTemplates.forEach(function (line) {
            if (line.text.indexOf(placeholder) === -1) return;
            line.el.textContent = line.text.split(placeholder).join(replacement);
            line.el.classList.toggle('script-line-filled', filled);
        });

        if (copyButton) {
            copyButton.disabled = !filled;
            copyButton.title = filled ? '' : 'Enter the value above first';
        }

        // The same value is often one Mendel stores. Filling it in saves typing
        // it twice, and typing it twice differently is its own failure.
        if (credential && filled) {
            const field = document.getElementById('value_' + credential);
            if (field && (!field.value || field.dataset.autofilled === 'true')) {
                field.value = value;
                field.dataset.autofilled = 'true';
            }
        }
    }

    input.addEventListener('input', apply);
    apply();
})();
