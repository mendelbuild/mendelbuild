-- Hosting platforms table: stores available cloud platforms for demo deployment
-- This table is seeded on startup and can be refreshed via CLI
CREATE TABLE hosting_platforms (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug TEXT UNIQUE NOT NULL,           -- "fly-io", "cloud-run", "vercel"
    name TEXT NOT NULL,                  -- "Fly.io", "Google Cloud Run", "Vercel"
    deployer_image TEXT NOT NULL,        -- Docker image with /bin/sh (e.g., "alpine:latest")
    instructions TEXT NOT NULL,          -- AI prompt fragment for generating deploy scripts
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_hosting_platforms_slug ON hosting_platforms(slug);
