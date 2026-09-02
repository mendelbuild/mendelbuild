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
    // Both routes out of here are gated the same way: an unfilled placeholder is
    // a syntax error whether it is pasted or saved to a file.
    const outButtons = [
        document.querySelector('[data-copy="setup-script"]'),
        document.querySelector('[data-download-script]'),
    ].filter(Boolean);
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

        outButtons.forEach(function (b) {
            b.disabled = !filled;
            b.title = filled ? '' : 'Enter the value above first';
        });

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

// Offer the script as a file as well as a clipboard payload.
//
// Pasting six thousand characters of multi-line shell into an interactive prompt
// is not reliable, and the ways it fails are the shell's rather than the
// script's: bracketed paste, vi mode, and paste-magic plugins each handle
// embedded newlines differently, and a script that parses perfectly can sit at a
// continuation prompt doing nothing. A file has none of those problems.
(function () {
    const button = document.querySelector('[data-download-script]');
    const source = document.getElementById('setup-script');
    if (!button || !source) return;

    button.addEventListener('click', function () {
        // A file ends with a newline; a shell reading one without is entitled to
        // complain about the last line.
        let text = source.textContent;
        if (!text.endsWith('\n')) text += '\n';

        const url = URL.createObjectURL(new Blob([text], {type: 'text/x-shellscript'}));
        const link = document.createElement('a');
        link.href = url;
        link.download = 'mendel-setup.sh';
        document.body.appendChild(link);
        link.click();
        link.remove();
        setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
    });
})();
