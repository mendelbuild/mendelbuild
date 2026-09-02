package codegen

import (
	"fmt"
	"strings"

	"github.com/bhs/mendelbuild/internal/hosting"
)

// shortenPath removes common prefixes to make paths more readable.
func shortenPath(path string) string {
	// Remove /tmp/mendel/<uuid>/ prefix
	if strings.HasPrefix(path, "/tmp/mendel/") {
		parts := strings.SplitN(path, "/", 5)
		if len(parts) >= 5 {
			return parts[4]
		}
	}
	return path
}

// BuildImplementationPrompt constructs the prompt for implementing a variation.
// artifactKind specifies the deployment artifact type: "container", "kubernetes", "static", "source_deploy", or empty.
func BuildImplementationPrompt(hopName, variationName, approach, artifactKind string, wantsExperiment bool) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("# Task: Implement the '%s' variation for hop '%s'\n\n", variationName, hopName))
	prompt.WriteString("## Approach\n\n")
	prompt.WriteString(approach)
	prompt.WriteString("\n\n## Instructions\n\n")
	prompt.WriteString("1. Implement the approach described above\n")
	prompt.WriteString("2. Write clean code following existing style and patterns in this repository\n")
	prompt.WriteString("3. Create or modify files as needed\n")
	prompt.WriteString("4. Add simple unit tests if appropriate (Mendel will run them later)\n")
	prompt.WriteString("5. Stop when implementation is complete - do NOT run tests yourself\n")

	// Add deployment artifact instructions based on artifact kind
	if artifactKind != "" {
		prompt.WriteString("\n## Deployment Artifact\n\n")
		switch artifactKind {
		case "container":
			prompt.WriteString("This project deploys as a **Docker container**. Ensure a `Dockerfile` exists in the repo root.\n\n")
			prompt.WriteString("If no Dockerfile exists, create one appropriate for the project's stack.\n")
			prompt.WriteString("If one exists, update it if your changes require new dependencies or build steps.\n\n")
			prompt.WriteString("The Dockerfile should:\n")
			prompt.WriteString("- Build and run the application\n")
			prompt.WriteString("- Support a health check endpoint (/ or /health returning 200)\n\n")
			prompt.WriteString(fmt.Sprintf("**The app must listen on the port in the `PORT` environment variable**, "+
				"binding `0.0.0.0` rather than `127.0.0.1`. Every platform this deploys to sets `PORT` and routes "+
				"to it; an app that ignores it starts cleanly and is then unreachable, which the platform reports "+
				"as a refused connection rather than as a misconfiguration. Falling back to a default when `PORT` "+
				"is unset is fine and keeps local development working. Deployments set it to %d, so `EXPOSE` and "+
				"any Dockerfile `HEALTHCHECK` should use that same default to stay consistent.\n",
				hosting.ContainerPort))
		case "kubernetes":
			prompt.WriteString("This project deploys to **Kubernetes**. Ensure k8s manifests exist.\n\n")
			prompt.WriteString("Required files (in `k8s/` or repo root):\n")
			prompt.WriteString("- `deployment.yaml` - Kubernetes Deployment\n")
			prompt.WriteString("- `service.yaml` - Kubernetes Service (LoadBalancer or ClusterIP)\n\n")
			prompt.WriteString("The deployment should include readiness/liveness probes.\n")
		case "static":
			prompt.WriteString("This project deploys as **static files**.\n\n")
			prompt.WriteString("Ensure there's a build step that produces static assets (HTML/CSS/JS) in a `dist/` or `build/` directory.\n")
		case "source_deploy":
			prompt.WriteString("This project uses **source-based deployment** (platform builds from source).\n\n")
			prompt.WriteString("Ensure the project has appropriate config for the hosting platform:\n")
			prompt.WriteString("- Fly.io: `fly.toml`\n")
			prompt.WriteString("- Vercel: `vercel.json`\n")
			prompt.WriteString("- Render: `render.yaml`\n")
		}
		prompt.WriteString("\n")
	}

	prompt.WriteString("\n## IMPORTANT: `.mendel/` Directory Rules\n\n")
	prompt.WriteString("The `.mendel/` directory is for Mendel configuration ONLY. Allowed files:\n")
	prompt.WriteString("- `test-config.yml` / `docker-compose.test.yml` - Test config\n")
	prompt.WriteString("- `migration.json` - Migration instructions (if schema changes needed)\n")
	prompt.WriteString("- `requirements.json` - What the code needs in order to run (if anything)\n")
	// Listed only when asked for. Naming a file in the allowed set is an
	// invitation to create it, and a variation that declares a live experiment
	// unasked would put real traffic on a comparison nobody designed.
	if wantsExperiment {
		prompt.WriteString("- `experiment.json` - Live-traffic experiment declaration (see below)\n")
	}
	prompt.WriteString("\n")
	prompt.WriteString("**DO NOT create any other files in `.mendel/`** - no documentation, no summaries.\n")

	prompt.WriteString("\n## Testing (Optional)\n\n")
	prompt.WriteString("If you add tests, create `.mendel/test-config.yml`:\n")
	prompt.WriteString("```yaml\n")
	prompt.WriteString("version: 1\n")
	prompt.WriteString("service: app\n")
	prompt.WriteString("test_command: npm test  # or appropriate command\n")
	prompt.WriteString("```\n\n")
	prompt.WriteString("Keep tests simple and self-contained. Do NOT:\n")
	prompt.WriteString("- Write integration tests requiring external APIs\n")
	prompt.WriteString("- Try to run or verify tests yourself\n")
	prompt.WriteString("- Write tests that need real database connections\n\n")
	prompt.WriteString("Mendel will run tests in Docker after code generation.\n")

	prompt.WriteString("\n## Database/Datastore Migrations\n\n")
	prompt.WriteString("If this variation requires database or datastore schema changes:\n\n")
	prompt.WriteString("1. Create a migration file following the project's existing conventions\n")
	prompt.WriteString("2. Create `.mendel/migration.json`:\n")
	prompt.WriteString("```json\n")
	prompt.WriteString("{\n")
	prompt.WriteString("  \"up_instructions\": \"Command to apply the migration\",\n")
	prompt.WriteString("  \"down_instructions\": \"Command to revert the migration\",\n")
	prompt.WriteString("  \"notes\": \"Path to migration files\"\n")
	prompt.WriteString("}\n")
	prompt.WriteString("```\n\n")
	prompt.WriteString("If no schema changes needed, skip migration steps entirely.\n")

	// The upstream half of live experiments. Only reachable when the caller asks
	// for it: an ordinary Variation is deployed whole, and declaring an
	// experiment it was not asked for would put real traffic on a comparison
	// nobody designed.
	if wantsExperiment {
		prompt.WriteString("\n## Live-Traffic Experiment\n\n")
		prompt.WriteString("This variation will run against real traffic beside the current code, ")
		prompt.WriteString("so create `.mendel/experiment.json`:\n")
		prompt.WriteString("```json\n")
		prompt.WriteString("{\n")
		prompt.WriteString("  \"assignment_unit\": \"user\",\n")
		prompt.WriteString("  \"assignment_key\": {\"source\": \"cookie\", \"name\": \"session_id\"},\n")
		prompt.WriteString("  \"migration\": {\n")
		prompt.WriteString("    \"up\": \"ALTER TABLE orders ADD COLUMN mendel_exp_<arm>_score INT;\",\n")
		prompt.WriteString("    \"down\": \"ALTER TABLE orders DROP COLUMN mendel_exp_<arm>_score;\"\n")
		prompt.WriteString("  },\n")
		prompt.WriteString("  \"dissonance\": \"What a person who saw this variation notices when it stops.\"\n")
		prompt.WriteString("}\n")
		prompt.WriteString("```\n\n")

		prompt.WriteString("Rules, all of which are checked and any of which will reject the variation:\n\n")
		prompt.WriteString("- **`assignment_unit`** is what one participant is: `user`, `session`, `request` ")
		prompt.WriteString("or `tenant`. Say what the application actually keys its data by, not what sounds ")
		prompt.WriteString("best: this same value decides how traffic is split and how results are counted, ")
		prompt.WriteString("and a wrong one makes the comparison meaningless rather than merely imprecise.\n")
		prompt.WriteString("- **`assignment_key`** is where that identity can be read at the edge, before ")
		prompt.WriteString("any of this variation's code runs.\n")
		prompt.WriteString("- **The migration must be purely additive.** Add columns, tables and indexes; ")
		prompt.WriteString("never drop, rename or change a type. The existing code keeps running against ")
		prompt.WriteString("the same schema throughout, so anything it can observe must not move.\n")
		prompt.WriteString("- **Every object you create must be prefixed `mendel_exp_`.** Other variations ")
		prompt.WriteString("of this hop are applying their own migrations to the same database at the same ")
		prompt.WriteString("time; the prefix is what stops two of them colliding.\n")
		prompt.WriteString("- **The `down` must undo the `up` exactly.** It is what allows the variation to ")
		prompt.WriteString("be withdrawn, and one that cannot be withdrawn will not be run.\n")
		prompt.WriteString("- **`assignment_unit: request` forbids a migration.** One person would meet both ")
		prompt.WriteString("versions, so per-participant writes are incoherent. Omit `migration` entirely.\n")
		prompt.WriteString("- **`dissonance`** is what a real person notices when this variation is taken ")
		prompt.WriteString("away mid-use. Write it plainly; a human reads it and types a phrase to accept it.\n\n")
		prompt.WriteString("If this variation changes nothing about the schema, omit `migration` and keep ")
		prompt.WriteString("the rest.\n")
	}

	prompt.WriteString("\n## What the Code Needs to Run\n\n")
	prompt.WriteString("If your changes mean the app cannot function without something a human must\n")
	prompt.WriteString("supply or set up elsewhere — OAuth credentials, an API key, a redirect URI\n")
	prompt.WriteString("registered in someone's console — declare it in `.mendel/requirements.json`.\n")
	prompt.WriteString("Mendel collects these before it deploys, so an undeclared one becomes a demo\n")
	prompt.WriteString("that starts and then fails in a way nobody can diagnose.\n\n")
	prompt.WriteString("```json\n")
	prompt.WriteString("{\n")
	prompt.WriteString("  \"requirements\": [\n")
	prompt.WriteString("    {\n")
	prompt.WriteString("      \"kind\": \"secret\",\n")
	prompt.WriteString("      \"name\": \"GOOGLE_CLIENT_SECRET\",\n")
	prompt.WriteString("      \"description\": \"OAuth client secret for Google sign-in\",\n")
	prompt.WriteString("      \"console_url\": \"https://console.cloud.google.com/apis/credentials\"\n")
	prompt.WriteString("    },\n")
	prompt.WriteString("    {\n")
	prompt.WriteString("      \"kind\": \"acknowledgement\",\n")
	prompt.WriteString("      \"name\": \"google-redirect-uri\",\n")
	prompt.WriteString("      \"description\": \"Redirect URI must be registered before sign-in works\",\n")
	prompt.WriteString("      \"instructions\": \"Add {{deploy_url}}/auth/callback to Authorized redirect URIs.\",\n")
	prompt.WriteString("      \"console_url\": \"https://console.cloud.google.com/apis/credentials\"\n")
	prompt.WriteString("    }\n")
	prompt.WriteString("  ]\n")
	prompt.WriteString("}\n")
	prompt.WriteString("```\n\n")
	prompt.WriteString("Two kinds, and the difference matters:\n\n")
	prompt.WriteString("- `secret` — a value Mendel holds and injects as an environment variable at\n")
	prompt.WriteString("  deploy time. `name` is the exact env var the code reads.\n")
	prompt.WriteString("- `acknowledgement` — an action taken somewhere else, which Mendel cannot do\n")
	prompt.WriteString("  and only needs confirmed. `name` is a slug; `instructions` say what to do.\n")
	prompt.WriteString("  Write `{{deploy_url}}` where the deployment's URL belongs — Mendel substitutes\n")
	prompt.WriteString("  the real one, which differs between the demo and production.\n\n")
	prompt.WriteString("Declare only what the code genuinely cannot run without. Anything with a\n")
	prompt.WriteString("working default, or that only affects an optional path, is not a requirement.\n")
	prompt.WriteString("If your changes need nothing, do not create the file.\n")

	prompt.WriteString("\n## Output\n\n")
	prompt.WriteString("Implement the changes directly. When done, provide a brief summary.\n")

	return prompt.String()
}

// BuildRevisionPrompt constructs a prompt for applying user feedback to an existing variation.
func BuildRevisionPrompt(hopName, variationName, approach, feedback string) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("# Task: Revise the '%s' variation for hop '%s'\n\n", variationName, hopName))

	prompt.WriteString("## Original Approach\n\n")
	prompt.WriteString(approach)
	prompt.WriteString("\n\n")

	prompt.WriteString("## User Feedback\n\n")
	prompt.WriteString("The user has requested the following change:\n\n")
	prompt.WriteString("> " + strings.ReplaceAll(feedback, "\n", "\n> "))
	prompt.WriteString("\n\n")

	prompt.WriteString("## Instructions\n\n")
	prompt.WriteString("1. This variation has already been implemented - review the existing code\n")
	prompt.WriteString("2. Make the changes requested in the user feedback above\n")
	prompt.WriteString("3. Follow existing code style and patterns\n")
	prompt.WriteString("4. Update or add tests if appropriate\n")
	prompt.WriteString("5. Stop when changes are complete - do NOT run tests yourself\n\n")

	prompt.WriteString("Focus on addressing the specific feedback. Make minimal changes beyond what's needed.\n")

	return prompt.String()
}

// BuildResumePrompt continues a run that was paused at its spend ceiling.
//
// The conversation from the paused run is gone -- for a coding agent the
// durable state is the files on disk, not the transcript -- so this asks the
// model to re-orient by reading its own half-finished work. That is a task it
// is good at, and it costs one pass rather than the whole run again.
//
// The prompt says the work was interrupted for cost rather than because
// anything was wrong, so the model finishes what it started instead of second
// guessing it, and it names the budget so the model prioritises rather than
// starting something new it cannot complete.
func BuildResumePrompt(hopName, variationName, approach, artifactKind string) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("# Task: Finish the '%s' variation for hop '%s'\n\n", variationName, hopName))

	prompt.WriteString("## Intended Approach\n\n")
	prompt.WriteString(approach)
	prompt.WriteString("\n\n")

	prompt.WriteString("## What happened\n\n")
	prompt.WriteString("A previous run started this work and was interrupted partway through ")
	prompt.WriteString("because it reached its cost budget. Nothing was found to be wrong with it. ")
	prompt.WriteString("The code it wrote is still in the working directory, and you are continuing it.\n\n")

	prompt.WriteString("## Instructions\n\n")
	prompt.WriteString("1. Start by reading the existing code to work out what has already been done\n")
	prompt.WriteString("2. Continue from there - do not restart the work or rewrite what is already correct\n")
	prompt.WriteString("3. Prioritise finishing the approach above over polishing what exists\n")
	prompt.WriteString("4. Follow existing code style and patterns\n")
	prompt.WriteString("5. Stop when the approach is fully implemented - do NOT run tests yourself\n\n")

	if artifactKind != "" {
		prompt.WriteString(fmt.Sprintf("Target artifact kind: %s\n\n", artifactKind))
	}

	prompt.WriteString("This run has its own budget. Finish the most important remaining work first, ")
	prompt.WriteString("so that if you are interrupted again the variation is as close to complete as possible.\n")

	return prompt.String()
}
