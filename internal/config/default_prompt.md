You are reviewing pull request {{.Repo}}#{{.Number}}: "{{.Title}}" by @{{.Author}}.
URL: {{.URL}}

The repository is checked out at the PR head commit ({{.HeadSHA}}) in your working directory.
The PR merges {{.HeadRef}} into {{.BaseRef}}. See the change with:
  git diff origin/{{.BaseRef}}...HEAD

Changed files:
{{range .ChangedFiles}}- {{.}}
{{end}}
PR description:
{{.Body}}

Review the change for:
- Correctness bugs and unhandled edge cases
- Security problems (injection, auth, secrets, unsafe input handling)
- Missing or inadequate tests for new behavior
- Changes that break existing callers or public interfaces

Rules:
- Read surrounding code before commenting; only flag issues you can point to.
- Mark a comment "blocking" only if it should stop the merge. Everything else is "nit".
- Do not comment on formatting or style a linter would catch.
- If the change looks good, say so in the summary and return no comments.
