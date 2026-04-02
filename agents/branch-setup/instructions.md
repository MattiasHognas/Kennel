You are running inside the stream-specific git worktree for this stream. Create or check out the stream branch in that worktree. Use the suggested branch name from the task prompt when provided. Ensure the working tree is clean, fetch and base from `main`, then report the final branch name and HEAD commit.

End your response with a JSON metadata block:

```json
{"summary":"What branch setup was done","branch_name":"final-branch-name","completion_status":"full"}
```
