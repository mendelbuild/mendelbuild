-- Whether a platform hands a deployment a public hostname is the difference
-- between a demo that can complete an OAuth round trip and one that cannot, and
-- it was invisible: Fly.io gives *.fly.dev, Cloud Run gives *.run.app, and GKE
-- gives a bare LoadBalancer address that no identity provider will accept.
--
-- Recording it lets Mendel say so when the channel is chosen, rather than
-- letting the user discover it in a provider's console after deploying.
ALTER TABLE hosting_platforms
    ADD COLUMN hostname_source TEXT NOT NULL DEFAULT 'platform'
        CHECK (hostname_source IN ('platform', 'user'));
