// Reload the experiments page when what it says has changed.
//
// The readiness checks reach a cluster and a database, so they run behind the
// render. A cold page therefore shows "Checking" and, without this, would sit
// there until someone reloaded -- and an install started from this page takes a
// minute or two to change anything, which is exactly when a person is watching.
//
// A fingerprint rather than a diff: the server sends a short string that changes
// only when something would look different, so a refresh that finds nothing new
// reloads nothing.
(function () {
    const root = document.getElementById('experiment-readiness');
    if (!root) return;

    const url = root.getAttribute('data-status');
    let current = root.getAttribute('data-fingerprint');
    if (!url) return;

    // Long enough to cover a controller install, which is the slowest thing this
    // page starts. Then it stops rather than polling at an unattended tab
    // forever.
    let remaining = 80;

    function check() {
        if (remaining-- <= 0) return;

        fetch(url, {headers: {'Accept': 'application/json'}})
            .then(function (r) { return r.ok ? r.json() : null; })
            .then(function (status) {
                if (status && status.fingerprint && status.fingerprint !== current) {
                    // Everything on this page is derived from the observation, so
                    // reloading is both simplest and complete -- no markup lives
                    // in two places waiting to drift.
                    window.location.reload();
                    return;
                }
                setTimeout(check, 3000);
            })
            .catch(function () { setTimeout(check, 3000); });
    }

    setTimeout(check, 2000);
})();
