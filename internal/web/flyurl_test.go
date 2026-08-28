package web

import "testing"

// TestFlyDeployedURL covers the output shapes that produced wrong demo URLs in
// staging. Both failures came from taking the first https:// in the output:
// flyctl prints its dashboard link before the app URL, and the build log
// carries whatever the project's own toolchain emits.
func TestFlyDeployedURL(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		appName string
		want    string
	}{
		{
			name:    "dashboard link precedes the app URL",
			appName: "pong-game-0e30d7df",
			// Shape taken from the deploy that recorded a monitoring URL.
			output: `Checking DNS configuration for pong-game-0e30d7df.fly.dev
You can monitor your deployment at https://fly.io/apps/pong-game-0e30d7df/monitoring
Visit your newly deployed app at https://pong-game-0e30d7df.fly.dev/`,
			want: "https://pong-game-0e30d7df.fly.dev",
		},
		{
			name:    "build log URLs are not the app",
			appName: "pong-game-0e30d7df",
			// An earlier deploy recorded npm's release notes as the demo URL.
			output: `npm notice New major version available!
npm notice Changelog: https://github.com/npm/cli/releases/tag/v12.0.2
Visit your newly deployed app at https://pong-game-0e30d7df.fly.dev/`,
			want: "https://pong-game-0e30d7df.fly.dev",
		},
		{
			name:    "no marker falls back to the app's own fly.dev host",
			appName: "pong-game-0e30d7df",
			output: `You can monitor your deployment at https://fly.io/apps/pong-game-0e30d7df/monitoring
Checking DNS configuration for pong-game-0e30d7df.fly.dev`,
			want: "https://pong-game-0e30d7df.fly.dev",
		},
		{
			name:    "no usable URL constructs one from the app name",
			appName: "pong-game-0e30d7df",
			output:  "Deploying pong-game-0e30d7df\nbuilding image...\ndone",
			want:    "https://pong-game-0e30d7df.fly.dev",
		},
		{
			name:    "a dashboard-only output never yields a fly.io URL",
			appName: "some-app",
			output:  `Monitor at https://fly.io/apps/some-app/monitoring and docs at https://fly.io/docs/`,
			want:    "https://some-app.fly.dev",
		},
		{
			name:    "trailing slash is trimmed",
			appName: "app",
			output:  "Visit your newly deployed app at https://app.fly.dev/",
			want:    "https://app.fly.dev",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := flyDeployedURL(c.output, c.appName); got != c.want {
				t.Errorf("flyDeployedURL() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestFlyDeployedURLNeverReturnsDashboard is the property that actually
// matters: a fly.io link is a Mendel-operator page, not the running demo, so
// handing one to a reviewer as "the demo" is always wrong.
func TestFlyDeployedURLNeverReturnsDashboard(t *testing.T) {
	outputs := []string{
		`https://fly.io/apps/x/monitoring`,
		`https://fly.io/docs/getting-started/`,
		`monitor: https://fly.io/apps/x/monitoring
Visit your newly deployed app at https://x.fly.dev/`,
		`https://github.com/npm/cli/releases/tag/v12.0.2`,
	}
	for _, out := range outputs {
		got := flyDeployedURL(out, "x")
		if got != "https://x.fly.dev" {
			t.Errorf("output %q produced %q, want the app's own host", out, got)
		}
	}
}
