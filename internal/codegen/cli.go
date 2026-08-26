package codegen

import (
	"fmt"
	"strings"
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
func BuildImplementationPrompt(hopName, variationName, approach string) string {
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

	prompt.WriteString("\n## IMPORTANT: `.mendel/` Directory Rules\n\n")
	prompt.WriteString("The `.mendel/` directory is for Mendel configuration ONLY. Allowed files:\n")
	prompt.WriteString("- `demo-hosting.yml` - Cloud demo settings\n")
	prompt.WriteString("- `deploy-demo.sh` / `teardown-demo.sh` - Deploy scripts\n")
	prompt.WriteString("- `test-config.yml` / `docker-compose.test.yml` - Test config\n")
	prompt.WriteString("- `migration.json` - Migration instructions (if schema changes needed)\n\n")
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
