# Claude Code CLI - Linux Headless Guide

This document provides a comprehensive guide for using the headless mode (Linux) of Claude Code CLI. This mode is a non-interactive automation mode optimized for hands-off task execution in server environments, CI/CD pipelines, and automated scripts.

## Prerequisites & Installation

Before using Claude in headless mode, ensure the CLI is installed and configured:

**Installation verification:**
```bash
claude --version
```

**First-time setup:** 
If not installed, run:
```bash
npm install -g @anthropic-ai/claude-code
```

## Permission Modes

Claude Code uses permission modes to control what operations are permitted in headless mode. Set via the `--permission-mode` flag:

| Mode | Description |
|------|-------------|
| `acceptEdits` | Automatically accepts file edit permissions for the session. Still requires approval for shell commands. (Recommended for programming tasks) |
| `default` | Standard behavior - prompts for permission on first use of each tool. Safe for exploration and analysis tasks. |
| `plan` | Read-only analysis mode. Claude can explore and analyze but cannot modify files or execute commands. Useful for code review and architecture analysis. |
| `bypassPermissions` | Skips ALL permission prompts. **⚠️ WARNING:** Only use in externally sandboxed environments (containers, VMs). NEVER use on a local dev machine without proper isolation. Use with `--allowedTools` to restrict specific tools for safety. |

## Claude Code CLI Commands

### Basic Headless Execution
Use the `--print` (or `-p`) flag to run in non-interactive mode:
```bash
claude -p "analyze the codebase structure and explain the architecture"
```

### Tool Permissions
Control which tools Claude can use with `--allowedTools` and `--disallowedTools`:

```bash
# Allow specific tools
claude -p "stage my changes and write commits" \
  --allowedTools "Bash,Read" \
  --permission-mode acceptEdits

# Allow multiple tools (space-separated)
claude -p "implement the feature" \
  --permission-mode acceptEdits \
  --allowedTools Bash Read Write Edit

# Allow tools with restrictions (comma-separated string)
claude -p "run tests" \
  --permission-mode acceptEdits \
  --allowedTools "Bash(npm test),Read"

# Disallow specific tools
claude -p "analyze the code" \
  --disallowedTools "Bash,Write"
```

### Using Permission Modes
Control how permissions are handled effectively:
```bash
# Accept file edits automatically (recommended for programming)
claude -p "implement the user authentication feature" \
  --permission-mode acceptEdits \
  --allowedTools "Bash,Read,Write,Edit"

# Combine with allowed tools for safe automation
claude -p "fix the login flow bug" \
  --permission-mode acceptEdits \
  --allowedTools "Read,Write,Edit,Bash(npm test)"
```

### Output Formats

**Text Output (Default)**
```bash
claude -p "explain file src/components/Header.tsx"
```

**JSON Output**
Returns structured data including metadata (useful for CI/CD parsing):
```bash
claude -p "how does the data layer work?" --output-format json
```
*Example response:*
```json
{
  "type": "result",
  "subtype": "success",
  "total_cost_usd": 0.003,
  "is_error": false,
  "duration_ms": 1234,
  "duration_api_ms": 800,
  "num_turns": 6,
  "result": "The response text here...",
  "session_id": "abc123"
}
```

**Streaming JSON Output**
Streams each message as it is received:
```bash
claude -p "build an application" \
  --permission-mode acceptEdits \
  --output-format stream-json
```

### Multi-Turn Conversations
For multi-turn conversations, you can resume or continue sessions:

```bash
# Continue the most recent conversation
claude --continue --permission-mode acceptEdits "now refactor this for better performance"

# Resume a specific conversation by session ID
claude --resume 550e8400-e29b-41d4-a716-446655440000 \
  --permission-mode acceptEdits "update the tests"

# Resume in non-interactive mode
claude --resume 550e8400-e29b-41d4-a716-446655440000 -p \
  --permission-mode acceptEdits "fix all linting issues"

# Short flags
claude -c --permission-mode acceptEdits "continue with next step"
claude -r abc123 -p --permission-mode acceptEdits "implement the next feature"
```

### Advanced Configuration

**System Prompt Customization:** Append custom instructions to the system prompt.
```bash
claude -p "review this code" \
  --append-system-prompt "Focus on security vulnerabilities and performance issues"
```

**MCP Server Configuration:** Load MCP servers from a JSON configuration file.
```bash
claude -p "analyze the metrics" \
  --mcp-config monitoring-tools.json \
  --allowedTools "mcp__datadog,mcp__prometheus"
```

**Verbose Logging:** Enable verbose output for debugging.
```bash
claude -p "debug this issue" --verbose
```

### Combined Examples
Combine multiple flags for complex scenarios:

```bash
# Full automation with JSON output
claude -p "implement authentication and output results" \
  --permission-mode acceptEdits \
  --allowedTools "Bash,Read,Write,Edit" \
  --output-format json

# Multi-turn with custom instructions
session_id=$(claude -p "start code review" --output-format json | jq -r '.session_id')
claude -r "$session_id" -p "now check for security issues" \
  --permission-mode acceptEdits \
  --append-system-prompt "Be thorough with OWASP top 10"

# Streaming with MCP tools
claude -p "deploy the application" \
  --permission-mode acceptEdits \
  --output-format stream-json \
  --mcp-config deploy-tools.json \
  --allowedTools "mcp__kubernetes,mcp__docker"
```

### Error Handling
- Check exit codes and stderr for errors.
- Use timeouts for long-running operations:
  ```bash
  timeout 300 claude -p "$complex_prompt" --permission-mode acceptEdits || echo "Timed out after 5 minutes"
  ```
- Respect rate limits when making multiple requests by adding delays between calls.

## Example Usage Scenarios

**1. Code Analysis (Read-Only)**
- **Prompt:** "Count the lines of code in this project by language"
- **Command:**
  ```bash
  claude -p "count the total number of lines of code in this project, broken down by language" \
    --allowedTools "Read,Bash(find),Bash(wc)"
  ```
- **Action:** Searches all files, categorizes by extension, counts lines, reports totals.

**2. Bug Fixing**
- **Prompt:** "Fix the authentication bug in the login flow"
- **Command:**
  ```bash
  claude -p "fix the authentication bug in the login flow" \
    --permission-mode acceptEdits \
    --allowedTools "Bash,Read,Write,Edit"
  ```
- **Action:** Finds the bug, implements fix, runs tests.

**3. Feature Implementation**
- **Prompt:** "Implement dark mode support for the UI"
- **Command:**
  ```bash
  claude -p "add dark mode support to the UI with theme context and style updates" \
    --permission-mode acceptEdits \
    --allowedTools "Bash,Read,Write,Edit"
  ```
- **Action:** Identifies components, adds theme context, updates styles, tests in both modes.

**4. Batch Operations**
- **Prompt:** "Update all imports from old-lib to new-lib"
- **Command:**
  ```bash
  claude -p "update all imports from old-lib to new-lib across the entire codebase" \
    --permission-mode acceptEdits \
    --allowedTools "Read,Write,Edit,Bash(npm test)"
  ```
- **Action:** Finds all imports, performs replacements, verifies syntax, runs tests.

**5. Generate Report with JSON Output**
- **Prompt:** "Analyze security vulnerabilities and output as JSON"
- **Command:**
  ```bash
  claude -p "analyze the codebase for security vulnerabilities and provide a detailed report" \
    --allowedTools "Read,Grep" \
    --output-format json
  ```
- **Action:** Scans code, identifies issues, outputs structured JSON with findings.

**6. SRE Incident Response**
- **Prompt:** "Investigate the payment API errors"
- **Command:**
  ```bash
  claude -p "Incident: Payment API returning 500 errors (Severity: high)" \
    --append-system-prompt "You are an SRE expert. Diagnose the issue, assess impact, and provide immediate action items." \
    --output-format json \
    --allowedTools "Bash,Read,mcp__datadog" \
    --mcp-config monitoring-tools.json
  ```
- **Action:** Analyzes logs, identifies root cause, provides action items.

**7. Automated Security Review for PRs**
- **Prompt:** "Review the current PR for security issues"
- **Command:**
  ```bash
  gh pr diff | claude -p \
    --append-system-prompt "You are a security engineer. Review this PR for vulnerabilities, insecure patterns, and compliance issues." \
    --output-format json \
    --allowedTools "Read,Grep"
  ```
- **Action:** Analyzes diff, identifies security issues, outputs structured report.

**8. Multi-Turn Legal Document Review**
- **Command:**
  ```bash
  # Start session and capture ID
  session_id=$(claude -p "start legal review session" --output-format json | jq -r '.session_id')

  # Review in multiple steps
  claude -r "$session_id" -p "review contract.pdf for liability clauses" --permission-mode acceptEdits
  claude -r "$session_id" -p "check compliance with GDPR requirements" --permission-mode acceptEdits
  claude -r "$session_id" -p "generate executive summary of risks" --permission-mode acceptEdits
  ```
- **Action:** Multi-turn analysis with context preservation.

## When Automation Pauses (Interrupts)

Lưu ý quan trọng: Ngay cả khi chạy ở chế độ headless (không tương tác), tiến trình vẫn có thể tạm dừng và chờ user nhập xác nhận (gây treo script) nếu gặp các trường hợp đặc biệt nhạy cảm:
- **Destructive operations:** Xóa database, force push git lên nhánh main, drop tables.
- **Security decisions:** Lộ thông tin credentials, thay đổi cấu hình xác thực, mở port mạng.
- **Ambiguous requirements:** Yêu cầu mơ hồ có nhiều cách giải quyết với rủi ro/trade-off lớn.
- **Missing critical information:** Không thể tiếp tục nếu thiếu dữ liệu (ví dụ: API key).

## Resumable Execution
If an automated execution is interrupted or needs follow-up, you can resume it using the Session ID outputted in the JSON result:
```bash
claude --resume <session_id> -p "continue" --permission-mode acceptEdits
```
