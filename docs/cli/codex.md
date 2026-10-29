# OpenAI Codex CLI - Linux Headless Guide

This document provides a concise guide for installing and using the headless mode (Linux) of OpenAI Codex CLI. This tool is optimized for server environments, CI/CD pipelines, and automation scripts without a graphical UI.

## Install

```bash
curl -fsSL https://chatgpt.com/codex/install.sh | sh
```

**Verify (Requires v0.57.0+ for GPT-5.2 support):**
```bash
codex --version
```

**Start:**
```bash
codex
```

## Authentication

**Device Code Login (Recommended for Headless):**
```bash
codex login --device-auth
```
*Lưu ý: Bạn cần bật tính năng xác thực mã thiết bị (device code authentication) cho Codex trong Cài đặt bảo mật (Security Settings).*

**Alternative - Set API key:**
```bash
export OPENAI_API_KEY="your_api_key"
```

**Permanent API key:**
```bash
echo 'export OPENAI_API_KEY="your_api_key"' >> ~/.bashrc
source ~/.bashrc
```

## Headless Mode

**Execute one task**
```bash
codex exec --sandbox read-only --skip-git-repo-check "Analyze this repository" 2>/dev/null
```
*(By default, append `2>/dev/null` to suppress stderr thinking tokens unless debugging).*

**Specifying a Model & Reasoning Effort**
```bash
codex exec -m gpt-5.2 \
  --config model_reasoning_effort="high" \
  --sandbox read-only \
  --skip-git-repo-check \
  "Analyze architecture" 2>/dev/null
```
*(Available models: `gpt-5.2-max`, `gpt-5.2`, `gpt-5.2-mini`, `gpt-5.1-thinking`. Reasoning effort: `xhigh`, `high`, `medium`, `low`)*

**Code modification**
```bash
codex exec --sandbox workspace-write --skip-git-repo-check --full-auto "Implement JWT authentication" 2>/dev/null
```

**Automation mode with broad access**
```bash
codex exec --sandbox danger-full-access --skip-git-repo-check --full-auto "Fix tests and commit changes" 2>/dev/null
```

**Resume a Session**
```bash
echo "Follow up prompt" | codex exec --skip-git-repo-check resume --last 2>/dev/null
```
*(Do not pass configuration flags like `--model` or `--sandbox` when resuming unless explicitly overriding).*

## Useful Commands

| Command | Purpose |
|---------|---------|
| `codex` | Interactive mode |
| `codex exec` | Headless execution |
| `codex app` | Desktop app |
| `codex --help` | Help |
| `codex update` | Update |

## Common Workflow

**Review code**
```bash
codex exec --sandbox read-only --skip-git-repo-check "Review current changes and report bugs" 2>/dev/null
```

**Generate tests**
```bash
codex exec --sandbox workspace-write --full-auto --skip-git-repo-check "Generate missing unit tests" 2>/dev/null
```

**Refactor**
```bash
codex exec --sandbox workspace-write --full-auto --skip-git-repo-check "Refactor this module safely" 2>/dev/null
```

## CI/CD Example
```bash
#!/bin/bash

# Dùng GPT-5.2, cấu hình effort high, cho phép ghi file vào workspace
codex exec \
-m gpt-5.2 \
--config model_reasoning_effort="high" \
--sandbox workspace-write \
--skip-git-repo-check \
--full-auto \
"Analyze CI failure, fix code, run tests" 2>/dev/null
```

## Claude Code Integration (Skill)

You can enable Claude Code to invoke the Codex CLI (`codex exec` and session resumes) for automated code analysis, refactoring, and editing workflows by installing the Codex skill.

**Prerequisites:**
- `codex` CLI installed and available on PATH.
- Codex configured with valid credentials (`codex --version` must run without errors).

**Installation:**
Run the following script to download and install the skill into Claude's skill directory:
```bash
git clone --depth 1 git@github.com:skills-directory/skill-codex.git /tmp/skills-temp && \
mkdir -p ~/.claude/skills && \
cp -r /tmp/skills-temp/ ~/.claude/skills/codex && \
rm -rf /tmp/skills-temp
```

**Example Workflow:**
1. **User Prompt:** `Use codex to analyze this repository and suggest improvements for my claude code skill.`
2. **Claude Code Action:** Claude will activate the Codex skill and:
   - Ask which model to use (default: `gpt-5.2`) and reasoning effort level (`low`, `medium`, `high`, `xhigh`).
   - Select the appropriate sandbox mode (defaults to `read-only`).
   - Run the command, for example:
     ```bash
     codex exec -m gpt-5.2 \
       --config model_reasoning_effort="high" \
       --sandbox read-only \
       --full-auto \
       --skip-git-repo-check \
       "Analyze this Claude Code skill repository comprehensively..." 2>/dev/null
     ```
3. **Result:** Claude will read the Codex output, summarize the analysis, and ask if you want to continue with follow-up actions (by piping a new prompt to `codex exec resume --last`).

