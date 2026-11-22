# The Three Departments and Six Ministries

> *"If names be not correct, language is not in accordance with the truth of
> things. If language be not in accordance with the truth of things, affairs
> cannot be carried on to success."*
> — Confucius, *Analects* 13.3 (名不正，則言不順；言不順，則事不成)

Asimi's structure is named after a real system of government. To understand why
Asimi is shaped the way it is, it helps to know what that system was, and which
of its ideas Asimi kept.

## The real thing: 三省六部

For much of the Tang dynasty (7th–10th century), the Chinese empire was run by
**three departments and six ministries** (三省六部). The design was a machine for
turning the emperor's will into governed action without letting any one official
accumulate unchecked power. The three departments each had a different job:

| Department | Chinese | Job |
|------------|---------|-----|
| **Secretariat** | 中書省 | **Drafts.** Turns the emperor's intent into formal documents (edicts). |
| **Chancellery** | 門下省 | **Review and veto (封駁).** Reads what the Secretariat drafted and may reject it. |
| **Department of State Affairs** | 尚書省 | **Executes.** Carries out the approved document through the six ministries. |

The third department ran **six ministries**, each with a domain:

| Ministry | Chinese | Domain |
|----------|---------|--------|
| Personnel | 吏部 | Appointments |
| Revenue | 戶部 | Taxation, census |
| **Rites** | 禮部 | Ritual, records, the written word |
| **War** | 兵部 | Military affairs |
| **Justice** | 刑部 | Law, judgment |
| **Works** | 工部 | Construction, engineering |

The genius of the system was the **veto**: the Chancellery could reject the
Secretariat's draft *before* the ministries ever acted on it. Drafting, review,
and execution were separate hands. No edict became law on one official's say-so.

## What Asimi kept

Asimi takes this very seriously, but it is a simplification — a working court for
one person's project, not a replication of a medieval empire. Here is the honest
mapping:

| Tang role | Asimi minister | What they do here |
|-----------|----------------|-------------------|
| Secretariat 中書省 | **Secretary** (中書令) | Talks to you; distills intent into edicts |
| Chancellery 門下省 | **Chancellor** (門下侍中) | Reviews every change; holds the veto (封駁) |
| Ministry of War 兵部 | **War** (兵部) | Plans the work; decomposes it into lings |
| Ministry of Works 工部 | **Forge** (工部) | Shapes the code (地) |
| Ministry of Justice 刑部 | **Judge** (刑部) | Runs tests; renders verdicts |
| Ministry of Rites 禮部 | **Rites** (禮部) | Owns the documentation |

> **A note on the veto.** In Asimi the Chancellor's 封駁 is real: it can reject a
> change against the *Imperial Code* — the accumulated laws and conventions of
> your codebase — and every ruling it makes is recorded as **precedent**, so the
> law grows case by case.

## Why any of this matters to your code

The Tang bureaucracy was not invented for elegance. It solved a coordination
problem: *how to act decisively without trusting any single actor completely.*
That is the same problem a coding agent faces.

- An unopinionated agent is a **tyrant** — fast, but unchecked.
- A court of ministers is a **government** — slower at any one step, but each
  step is checked, and the whole is accountable.

Asimi bets that for real work, on a real codebase, **the government wins.**

The next chapter begins the machinery: the **three realms**, the houses in
which every document, decision, and line of code lives.
