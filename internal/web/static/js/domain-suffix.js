// Keep the suffix beside each label field showing the domain actually typed.
//
// The suffix is rendered server-side too, so the page is correct without this;
// this only keeps it true while someone edits the domain above, when the stale
// version would be actively misleading.
(function () {
    const base = document.getElementById('base_domain');
    const demo = document.getElementById('demo_subdomain');
    const suffixes = document.querySelectorAll('[data-domain-suffix]');
    const example = document.querySelector('[data-demo-example]');
    if (!base || !suffixes.length) return;

    const clean = function (value) {
        // The same tidying the server does, so what is shown matches what will
        // be saved rather than what was typed.
        return value.trim().toLowerCase()
            .replace(/^https?:\/\//, '')
            .split(/[/?#]/)[0]
            .replace(/^\.+|\.+$/g, '');
    };

    const update = function () {
        const domain = clean(base.value) || 'yourdomain.com';
        suffixes.forEach(function (el) { el.textContent = '.' + domain; });
        if (example) {
            const label = clean(demo && demo.value ? demo.value : '').split('.')[0] || 'mendel-demos';
            example.textContent = '<demo>.' + label + '.' + domain;
        }
    };

    base.addEventListener('input', update);
    if (demo) demo.addEventListener('input', update);
    update();
})();
