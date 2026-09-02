// Fill in the readiness ladder once Mendel has actually looked.
//
// The page renders from the last observation rather than waiting for a new one,
// because observing means starting a gcloud process and making an API call, and
// a settings page should not take seconds to arrive. On a first visit there is
// no last observation, so the ladder says "Checking" -- this replaces it when
// the refresh the render triggered comes back.
//
// Polls only while there is something to wait for, and gives up rather than
// hammering an endpoint that is not answering.
(function () {
    const host = document.getElementById('domain-ladder');
    if (!host || host.getAttribute('data-checking') !== 'true') return;

    const url = host.getAttribute('data-poll');
    if (!url) return;

    let attempts = 0;
    const maxAttempts = 15; // ~45s, longer than any observation should take.

    function poll() {
        if (attempts++ >= maxAttempts) return;

        fetch(url, {headers: {'Accept': 'text/html'}}).then(function (response) {
            if (!response.ok) return null;
            const stillChecking = response.headers.get('X-Mendel-Checking') === 'true';
            return response.text().then(function (html) {
                host.innerHTML = html;
                if (stillChecking) setTimeout(poll, 3000);
            });
        }).catch(function () {
            // A failed poll is not worth reporting: the ladder on screen is
            // still accurate as of when it was rendered, and reloading the page
            // is the obvious recovery.
            setTimeout(poll, 3000);
        });
    }

    setTimeout(poll, 1200);
})();
