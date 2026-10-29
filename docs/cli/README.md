# Headless CLI Tools: Overview & Comparison

This document provides a high-level comparison and recommended server setup for the three supported headless CLI tools. For detailed instructions on each tool, please refer to their respective guides:
- [Antigravity CLI Guide](antigravity.md)
- [Codex CLI Guide](codex.md)
- [Claude CLI Guide](claude.md)

---

## Quick Comparison (Linux Headless)

| Tool | Install | Headless invocation | Model Selection | Auto-approve flag | Best use |
|------|---------|----------------------|-----------------|--------------------|----------|
| Antigravity (`agy`) | `install.sh` | `agy -p "<task>"` | `--effort high\|medium\|low` | `--dangerously-skip-permissions` | Agent workflow + skills |
| Codex CLI (`codex`) | `install.sh` | `codex exec "<task>"` | `--model gpt-4o` | `--full-auto` | Automation + coding |
| Claude Code (`claude`) | `install.sh` | `claude -p "<task>"` | `--model claude-3-7-sonnet...` | `--permission-mode acceptEdits` | Long context + repo agent |

**Common trait**: all three take the prompt as a **literal text argument** in their basic headless form — none of them accept a bare file path as the prompt and read its contents automatically. If you need to hand a tool a large prompt without hitting the shell's `ARG_MAX`/`MAX_ARG_STRLEN` limits, write the task to a file and give the CLI a short literal instruction telling it to open and follow that file (all three are agentic and can read files themselves) — don't pass the file path as the entire prompt.

**Model Configurations:**
- **Antigravity**: Abstracts model selection via reasoning effort (`--effort high|medium|low`), auto-mapping to Gemini tiers.
- **Codex**: Supports explicit model assignment via `--model` or `OPENAI_MODEL` environment variable.
- **Claude Code**: Supports explicit model assignment via `--model` or `ANTHROPIC_MODEL` environment variable.

**Auto-approve flags differ per tool** — there is no shared "skip confirmation" flag name:
- Antigravity: `--dangerously-skip-permissions`
- Codex: `--full-auto`
- Claude: `--permission-mode acceptEdits` (tự động chấp nhận sửa file) kết hợp với `--allowedTools` để giới hạn các lệnh Bash. Nếu muốn skip tất cả, dùng `--permission-mode bypassPermissions` (cực kỳ cẩn thận).

> ⚠️ Treat all three auto-approve/allow-list flags the same way: only use them in controlled/sandboxed environments (CI, disposable containers), never on a machine with untrusted input reachable by the agent.

---

## MCP Server Configuration

All three CLIs can spawn a **local stdio MCP server** (no network listener required) — the recommended mode for a Docker sandbox, since host↔container network reachability (`host.docker.internal`, loopback) is unreliable across setups. Each tool reads a config file that maps a server name to a launch command.

| Tool | Config file | Scope |
|------|-------------|-------|
| Antigravity (`agy`) | Global server setup, or workspace-level `mcp_config.json` | Global or per-workspace; simplest path is the Interactive MCP Manager (`agy mcp`), manual file edit is the headless/scripted alternative |
| Codex CLI (`codex`) | `~/.codex/config.toml` (`[mcp_servers.<name>]` table) | Global |
| Claude Code (`claude`) | `.mcp.json` in the project root, or `--mcp-config <path>` flag (confirmed in [claude.md](claude.md)) | Per-project (checked into repo) or ad-hoc |

**Antigravity CLI** — supports both local stdio processes and remote host MCP server configurations. The simplest path to installing an MCP server is the Interactive MCP Manager; for headless/scripted setups (no TUI), edit the workspace-level `mcp_config.json` directly:
```json
// <workspace>/mcp_config.json
{
  "mcpServers": {
    "context": {
      "command": "/usr/local/bin/my-mcp-server",
      "args": []
    }
  }
}
```

**Codex CLI**:
```toml
# ~/.codex/config.toml
[mcp_servers.context]
command = "/usr/local/bin/my-mcp-server"
args = []
```

**Claude Code**:
```json
// .mcp.json (project root)
{
  "mcpServers": {
    "context": {
      "command": "/usr/local/bin/my-mcp-server",
      "args": []
    }
  }
}
```

> All three configs point at the same binary: bundle one stdio MCP server (e.g. `my-mcp-server`) into the sandbox image, and reference it identically across `agy`, `codex`, and `claude` — no per-tool server logic needed.

---

## Recommended Server Setup

```text
Linux Server
     |
     |
+----+----+
|         |
Codex   Claude
|         |
CI/CD   Review
|
Automation

Antigravity
|
Multi-agent workflow
```

### Production AI Dev Stack

**Architecture**
```bash
agy --dangerously-skip-permissions -p "Review architecture"
```

**Implementation**
```bash
codex exec "Implement logic"
```

**Review / Debug**
```bash
claude -p
```

**CI Pipeline (e.g. GitHub Actions)**
```text
GitHub Actions
      |
      +-- codex exec
      |
      +-- claude -p
```

*Bộ 3 công cụ này đặc biệt phù hợp cho Linux server không GUI, môi trường SSH, Docker, CI/CD pipeline, và các autonomous coding agent.*
