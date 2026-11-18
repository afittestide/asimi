# The Ruler's Guide — Quick Start for Unix Veterans

🎂 Happy 50th birthday to `vi`, the tool that will live forever 🎂

**Note:** This guide is for **users** who have installed Asimi via brew, one-liner installer, or binary. For contributing to Asimi itself, see README.md → Development.

## You Are the Ruler

In Asimi's Court, **you** are the Ruler (君主). The AI ministers work for you. You speak intent; they execute. This guide gets you productive in 10 minutes.

## The Three Realms (三界) — Your Mental Model

Think of the Court as mediating between three realms:

```
Ren (人) — Your Will
  │
  ▼
Earth (地) — Code
  │
  ▼
Heaven (天) — Truth - logs & test runs
```

| Realm | What It Is | Unix Equivalent |
|-------|------------|-----------------|
| **人 Ren (Humanity)** | Your intent, issues, decisions | The user at the terminal |
| **天 Tian (Heaven)** | Tests, logs, CI verdicts, event ledger | `/var/log`, test output, `dmesg` — immutable records |
| **地 Di (Earth)** | Source code, git commits, worktrees | The filesystem, working directory |

**Harmony** = Your intent (Ren) flows through verification (Heaven) into code (Earth) without friction.

**Zhengming (正名)** = "Rectification of Names." When the Court doesn't understand your intent, it **stops and asks**. It never guesses. *To guess is treason.*

---

## Quick Start

**Note:** This guide is for **users** who have installed Asimi via brew, one-liner installer, or binary download. For contributing to Asimi itself, see README.md → Development.

### Your First Edict

An **Edict** (詔令) is a work order. You issue it; ministers execute it through a **Ritual** (workflow).

#### Step 1: Open the Hunting Tab

Press `gt` till you get to the **Hunting** tab. You're now talking to the **Chancellor** (門下侍中) — your advisor who sees everything but changes nothing.

#### Step 2: State Your Intent

Type something like:

```
Add a --version flag that prints the build version and exits
```

The Chancellor will help you crystallize this into a proper edict and enact a ritual
to seal it.

When the edict shows **sealed**, the work is complete. Asimi uses a local SQLite database (`asimi.db`) to track edicts, manifests, and seals. The seals table records:

- **Judge's Seal** — Tests passed
- **Chancellor's Seal** — Code review approved  
- **Ruler's Seal** — Final approval (granted automatically when you confirm)

Check `git log` to see the committed changes. The edict is now ascended to Heaven.

---

## vi Keybindings You Already Know

Asimi speaks vi. These work in the prompt buffer:

| Key | Action | vi Equivalent |
|-----|--------|---------------|
| `Esc` | Normal mode | Same |
| `i` | Insert mode | Same |
| `:command` | Run command | Same |
| `Ctrl-b` | SCROLL mode & back | Like `Ctrl-U` |
| `Ctrl-f` | Scroll forward | Like `Ctrl-D` |
| `gt` / `gT` | Next/prev tab | Same |
| `1gt`, `2gt` | Jump to tab 1, 2 | Same |
| `/pattern` | Search | Same |
| `n` / `N` | Next/prev match | Same |

---

## Common Workflows

### "Fix a Bug" — Swift Strike (S-size)

For small, focused changes (< 50 lines):

```
Hunting tab: "Fix the null pointer in auth/handler.go line 42"
→ Chancellor creates edict
→ Secretary enacts swift-strike ritual
→ Forge fixes, Judge tests, Chancellor reviews
→ Sealed in ~5 minutes
```

### "Add a Feature" — Castle Siege (M-size)

For medium work (50-200 lines):

```
Hunting tab: "Add user logout endpoint"
→ Chancellor helps scope it
→ Secretary enacts castle-siege ritual
→ War plans, Forge implements, Judge tests, Chancellor reviews
→ Sealed in ~30 minutes
```

### "What's Happening?" — Court Status

```
Ruling tab: "What's the status of all edicts?"
→ Secretary reports active edicts, phases, pending questions
```

Or check a specific edict:

```
Ruling tab: "Status of edict-123456"
→ Shows phase, lings, test results
```

### "I'm Confused" — Ask the Chancellor

Hunting tab is for exploration. Ask:

- "Why did this edict fail?"
- "What's blocking progress?"
- "Show me the code changes from edict-789"
- "Is there a better way to structure this?"

The Chancellor sees all code, edicts, tests, and precedents.

---

## Zhengming — When Asimi Asks Questions

When the Court encounters ambiguity, it **halts** and asks:

```
Zhengming from Secretary:
"Should the logout confirmation use a modal dialog or inline message?"

Options:
  1. Modal dialog with Cancel/Logout buttons
  2. Inline message below the button
  3. No confirmation — logout immediately
  4. Keep chatting
```

**You respond.** Your answer is appended to the edict's intent, and work resumes.

**Why this matters:** The Court never guesses. If it's unclear, it stops. This prevents wasted work and ensures the code matches your intent.

To keep the number of zhengmings low, we make it the goal of your Secretary to free you to hunt for future edicts. There's a healthy tension between not guessing and freeing you, one that keeps the Secretary on his toes.

---

## The Ministers — Who Does What

| Minister | Role | When You'd Talk to Them |
|----------|------|------------------------|
| **Secretary (中書令)** | Coordinator, interfaces with you | Always — all requests go through Secretary |
| **Chancellor (門下侍中)** | Advisor + code reviewer, sees all, creates edicts, reviews code | Hunting tab — for exploration, edict creation, and code review |
| **War (兵部)** | Plans complex work | Indirectly — creates battle plans for M/L edicts |
| **Forge (工部)** | Writes code | Indirectly — you see their manifests in work logs |
| **Judge (刑部)** | Runs tests, validates | Indirectly — you see verdicts (pass/fail) |

**Note:** The Chancellor serves as both advisor and code reviewer — reviewing code quality and recording precedents. This consolidates the review function.

**Key insight:** You talk to **Secretary** (Ruling tab) and **Chancellor** (Hunting tab). The other ministers work behind the scenes.

---

## Troubleshooting — When Things Get Stuck

### Edict Won't Progress

**Check for Zhengming:**
```
Ruling tab: "Any pending zhengming?"
→ Secretary lists unanswered questions
```

**Check test failures:**
```
Ruling tab: "Why is edict-123 stuck in judging?"
→ Shows failing tests
```

### Ritual Failed After Retries

The Court will invoke a `report_failure` ritual:

```
A ritual failed after 3 retries for edict-123.
Failure point: step "forge" — compilation error in auth/handler.go:42
Suggested next steps:
  1. Fix the compilation error manually, then resume
  2. Request zhengming to clarify the approach
  3. Cancel the edict and create a new one
```

**You decide.** The Court waits for your direction.

### "I Want to Take Over"

You can always:
1. **Cancel the edict:** `Ruling tab: "Cancel edict-123"`
2. **Fix it yourself:** Edit the code, commit, and ask the Chancellor to review
3. **Create a new edict:** With corrected scope

The Court serves you — not the other way around.

---

## Philosophy — Why Confucius?

The Confucian metaphor isn't decoration. It encodes principles of
careful, principled software development where clarity precedes action
and quality is non-negotiable.

For a full explanation of the Five Constant Virtues (五常) and how they
map to the Court, see [intent/docs/court_guide.md → Confucian Foundations](court_guide.md).

---

## Next Steps

1. **Try a small edict** — Fix a typo, add a log statement
2. **Explore the Hunting tab** — Ask the Chancellor about the codebase
3. **Watch a ritual execute** — See how ministers coordinate
4. **Read the full guide** — `intent/docs/court_guide.md` for deep dives

Welcome to the Court, Ruler. The court awaits your command.

---

## Quick Reference Card

```
Tabs: 1=Ruling (Secretary), 2=Hunting (Chancellor)
Edict Phases: brewing → planning → forging → judging → reviewing → sealed
Rituals: swift-strike (S), castle-siege (M), grand-orchestration (L/XL)
Zhengming: When Asimi asks, answer — work resumes after
vi keys: Esc, i, :, gt, gT, /, n — all work as expected
```

**Common Commands:**
```
:help          — Show help
:context       — Token usage
:new           — New conversation
:resume        — Resume session
:edict         — Manage edicts (read, enact, seal, resume, cancel)
:tabnew forge  — Open Forge's work log
:export        — Useful for debugging
```

**Common Questions:**
```
"What's the status of edict-123?"
"Why did this edict fail?"
"Show me the code changes"
"Any pending zhengming?"
"Cancel edict-123"
```
