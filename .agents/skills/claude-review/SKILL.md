---
name: claude-review
description: Run a Claude Code review loop on local changes, summarize the review, apply fixes, resume the saved Claude session with an update, and iterate until concerns are resolved or intentionally deferred. Use when the user wants Claude Code involved as an external reviewer during implementation.
---

# Claude Review

## Overview

Use Claude Code as a live reviewer for local changes, then drive an explicit fix-and-rerun loop until the review is clean or the remaining comments are deliberate tradeoffs.

## Workflow

1. Confirm the review target

- Identify what Claude should review: latest commit, working tree diff, specific files, or a feature branch range.
- If the target is ambiguous, state the assumption you are using before invoking Claude.

2. Start Claude Code

- Run Claude Code locally from the repository root with an explicit review prompt.
- Use this default invocation unless there is a concrete reason to change it:
  - `claude --allow-dangerously-skip-permissions --dangerously-skip-permissions --model opus --effort high <PROMPT>`
- Ask Claude to review the actual code changes, not just the user’s summary.
- Prefer a prompt that asks for:
  - overall verdict
  - issues to address
  - what’s good
  - concrete file/function references
  - distinction between blocking issues and observations

3. Capture the session identifier

- Save the Claude session UUID shown at exit or from `claude --resume ...`.
- Treat the session UUID as part of the working state for the review loop.
- If Claude is still running in a PTY, poll it for progress instead of starting a second independent review.

4. Give live status updates while Claude runs

- While Claude is running, provide short status notes based only on visible PTY output.
- Good updates include:
  - still analyzing diff
  - reading files
  - writing issues section
  - review text has started
- Do not claim hidden reasoning that Claude did not print.

5. Summarize Claude’s review for the user

- After Claude finishes, summarize:
  - the verdict
  - the blocking issues
  - the non-blocking observations
  - the best parts of the change
- Separate Claude’s comments from your own judgment.
- If a comment is weak, say so directly and explain why.

6. Make an implementation plan

- Group Claude’s comments into:
  - fix now
  - discuss / intentional tradeoff
  - defer
- Prefer fixing correctness, concurrency, lifecycle, API consistency, validation, and observability issues first.
- Do not make churn-only changes just to silence the reviewer.

7. Apply fixes and validate locally

- Implement the chosen fixes in the repo.
- Run the local validation expected for the repository before asking Claude to re-review.
- Summarize for the user what changed and what intentionally did not change.

8. Resume the same Claude session

- Resume the prior Claude session with the saved UUID instead of starting from scratch when continuing the same review thread.
- Tell Claude exactly what you changed and, if relevant, what you chose not to change and why.
- Ask for a focused re-review of the updated diff.

9. Iterate deliberately

- Repeat this loop:
  - Claude review
  - summarize for user
  - decide
  - fix
  - validate
  - resume Claude
- Stop when:
  - Claude has no remaining concerns worth fixing, or
  - the remaining comments are intentional design choices and you can justify them clearly.

10. Close the round

- Report back to the user with:
  - final Claude verdict
  - what you changed
  - what you intentionally left as-is
  - any follow-up work that should happen later
  - the final Claude session UUID if future continuation may be useful

## Review Standards

- Treat Claude as a strong reviewer, not the final authority.
- Fix real engineering problems, not stylistic churn.
- Prefer one continuing review thread per change rather than many disconnected Claude runs.
- Keep the user informed between rounds; do not disappear into the loop.
- If Claude suggests something that conflicts with repo architecture or product direction, explain the tradeoff and decide explicitly.

## Prompt Pattern

Use a review prompt with this shape and adapt it to the task:

```text
claude --allow-dangerously-skip-permissions --dangerously-skip-permissions --model opus --effort high "<PROMPT>"
```

Where `<PROMPT>` is:

```text
Please review the latest changes in this repository as a code review.

Context:
- Review the actual code changes in <range>.
- Focus on correctness bugs, structural issues, concurrency problems,
  lifecycle issues, API contract issues, missing tests, and significant
  mismatches with intended runtime maturity.
- Keep the review grounded in the changed code only.

Output format:
1. Overall
2. Issues to address
3. What's good
```

## Response Pattern

When reporting Claude’s review back to the user:

- lead with the verdict
- list the must-fix issues first
- explain your plan before editing
- after fixes, summarize the delta before re-running Claude
- after the final round, state clearly whether the loop is done
