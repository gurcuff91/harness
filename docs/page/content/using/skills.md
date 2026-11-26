---
title: Skills
section: Using Harness
order: 130
summary: Write SKILL.md skills, where harness discovers them, how the agent and you invoke them, and how SYSTEM.md and AGENTS.md shape the prompt.
---

A skill is a Markdown guide for a specific kind of task. Harness lists the available skills in the system prompt (name and one-line description); when a task matches, the agent loads the full text with the `Skill` tool. You can also run one yourself with `/skill:<name>`.

## Writing a skill

A skill is a directory containing a `SKILL.md`. The directory name is the skill's name.

```text
.harness/agent/skills/release-notes/
├── SKILL.md
└── template.md
```

```markdown
---
description: Draft release notes from the git log since the last tag
---

# Release notes

1. Run `git describe --tags --abbrev=0` to find the last tag.
2. Run `git log <tag>..HEAD --oneline`.
3. Group the commits into Added / Changed / Fixed, following `template.md`.
```

- The description is taken from the front matter `description:` field (single line, or a `|` block). Without front matter, the first non-empty line of the body is used.
- The front matter is stripped from the content the agent loads.
- When a skill is loaded, the result starts with the skill's absolute directory, so relative paths such as `template.md` resolve against it.

## Where skills are found

Four directories are scanned, lowest to highest priority:

| Directory | Scope |
| --- | --- |
| `~/.agents/skills/` | Global, shared with other tools |
| `~/.harness/agent/skills/` | Global, harness |
| `<project>/.agents/skills/` | Project, shared |
| `<project>/.harness/agent/skills/` | Project, harness |

When two directories define the same skill name, the higher-priority one wins. Skills are read when a session is opened.

## Running a skill

```text
/skill:release-notes for v1.4
```

The skill text is sent to the agent as a prompt, followed by anything you typed after the command. The history shows only `Skill:release-notes for v1.4`, not the full skill body. If the agent is busy, the prompt is queued.

## SYSTEM.md

`SYSTEM.md` replaces the base system prompt. Lookup order:

1. `<project>/.harness/agent/SYSTEM.md`
2. `~/.harness/agent/SYSTEM.md`

The first one found is used; they are not merged. Precedence of the base prompt overall: a prompt given in code (`AgentOptions.SystemPrompt`) beats `SYSTEM.md`, which beats the built-in default.

## AGENTS.md

`AGENTS.md` is project context appended to the system prompt. Harness looks for it in the working directory and walks up at most three parent directories, using the first one it finds.

Prompt assembly order: base prompt, skills list, memory section, tool sections, working directory, `AGENTS.md`, the agent's directives, enabled [profiles](../sdk/profiles.md) directives, then input-channel directives.
