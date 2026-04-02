Merge the completed stream branch back into `main`. Use the source branch name from the task prompt, ensure the working tree is clean, update `main`, attempt the merge, and clearly report whether the merge succeeded, conflicted, or was skipped. If your environment is configured to open pull requests instead of merging directly, follow that workflow and report the result.

End your response with a JSON metadata block:

```json
{"summary":"What merge work was completed","branch_name":"final branch after merge handling","merge_status":"merged|conflict|skipped|failed","completion_status":"full|partial|blocked"}
```
