# The Ministers

> *"A man who has committed a mistake and doesn't correct it is committing
> another mistake."*
> — attributed to Confucius

Asimi's court is staffed by **six ministers**. Two of them you speak to
directly; the other four work behind the scenes. Each has a **domain**, a set
of **tools**, and a set of **acts of treason** — the things it must never do.

The division of labour is the whole point. It is what lets the court check
itself.

## The roster

| Minister | Chinese | Domain | You talk to them? |
|----------|---------|--------|-------------------|
| **Secretary** | 中書令 | Court business; distills your intent into edicts | **Yes — the Ruling tab** |
| **Chancellor** | 門下侍中 | Review, veto (封駁), precedent — read-only | **Yes — the Hunting tab** |
| **War** | 兵部 | Strategy: plans the work, decomposes it into lings | Indirectly |
| **Forge** | 工部 | 地 — shapes the code | Indirectly |
| **Judge** | 刑部 | 天 — tests, verdicts | Indirectly |
| **Rites** | 禮部 | 典籍 — documentation and the written word | Indirectly |

A minister is *defined in configuration*, not hard-coded. You can override any
of them (see [Config Files](../configuration/config.md)), but the shipped
roster is the one above.

## The two you meet

### The Secretary (中書令)

Your primary interface. The Secretary helps you **plan new features, hunt bugs,
and manage the court**. It does not change code — its job is to take your
intent and forge it into a clean **edict**: focused on *what* and *why*,
leaving the *how* to War and the Forge.

Its superpower is `asimisql` — direct, read-only queries against the court's
ledger. Anything you want to know about the state of the court, the Secretary
can find.

> **Two acts of treason for the Secretary:** guessing (it must `ask_ruler`
> when your intent is unclear) and *coding* (Earth is not its domain).

### The Chancellor (門下侍中)

Your advisor and your court's conscience. The Chancellor **sees everything and
changes nothing** — full read-only access to the code, the edicts, and every
court record. It:

- distills fuzzy intent into sharp edicts,
- reviews every change against the **Imperial Code**, and
- holds the **veto (封駁)** — it can reject a change that falls short.

Every ruling the Chancellor makes is recorded as **precedent**, searchable case
law that grows the law of your codebase. And at its seal it performs an
**omission sweep**: every `TODO`, `FIXME`, `XXX`, or `HACK` in the family of
files under review must be *handled* (turned into a ling with a durable id) or
*removed* (struck with a recorded reason). There is no holding pen, and removal
is never silent.

> **Acts of treason for the Chancellor:** guessing, **modifying code**
> (it is read-only), and issuing a ruling without written justification —
> every ruling becomes permanent precedent.

## The four behind the scenes

### War (兵部) — the strategist

War's domain is **strategy and sequence**. Summoned to plan, it decomposes an
edict into executable **lings** (令, task orders) with clear dependencies, and
enforces their order. All changes to a single file belong in a single ling.

> **Treason for War:** guessing; a ling that is not atomic, clear, and
> testable; introducing cycles in the dependency graph; using shorthand ling
> IDs.

### Forge (工部) — the maker

Forge's domain is **地** — simple, clear code forged into existence. It writes
the code, and records every change as a **manifest**. Inside an edict's
worktree, Forge commits its finished work on the worktree branch; those commits
never touch the main tree, because the Ruler's seal squash-merges the edict
into a single commit at ascension.

> **Treason for Forge:** non-idiomatic code; changing tests (that is the
> Judge's dispatch); committing on the main tree (the court must approve first);
> and finishing without recording a manifest.

### Judge (刑部) — the weigher

Judge's domain is **天** — test results and testing code. It reviews the
Forge's manifests against the working tree, and presides over the **verdicts**
table. Tests pass → the manifest is *quenched*. Tests fail → it is *rejected*,
with clear feedback for the Forge.

The Judge is **adversarial and data-driven**. Its word is final — but
*authoritative, not infallible*. It renders verdicts from evidence, cites that
evidence by coordinates, and never treats raw test output as a verdict.

> **Treason for the Judge:** guessing; rendering a verdict without evidence;
> mutating a verdict once rendered; presuming code innocent — *code is guilty
> until proven innocent*.

### Rites (禮部) — the pen

Rites' domain is **典籍** — the declared word made legible. It owns all
documentation: the guides you are reading, the in-terminal help, and the
developer internals. Where the Forge shapes 地 and the Judge weighs 天, Rites
renders the court's craft into **人** for the sovereign.

> **Treason for Rites:** guessing; and *letting a document describe behavior
> the code no longer has.*

## Permissions: the realm model

Every minister's tools derive from a **9-character permission string** — three
triplets following Unix `rwx` notation, one triplet per realm:

```
  r  w  x   r  w  x   r  -  -    
  └──地──┘  └──天──┘  └──人──┘    
  Earth     Heaven    Intent      
```

Read a minister's string and you know exactly what it can touch. Examples from
the real roster:

| Minister | Permission | 地 | 天 | 人 | Reading |
|----------|------------|----|----|----|---------|
| **Forge** | `rwxr---w-` | rwx | r-- | -w- | Full Earth; read Heaven; append Intent |
| **Judge** | `rwxrwxr--` | rwx | rwx | r-- | Full Earth & Heaven; read Intent |
| **Chancellor** | `r--r--rwx` | r-- | r-- | rwx | Read all; **write only Intent** |
| **War** | `r-----rw-` | r-- | --- | rw- | Read Earth; **no Heaven**; write Intent |
| **Secretary** | `r-xrwxrwx` | r-x | rwx | rwx | Broad; append-only on Earth |
| **Rites** | `rw-r--rw-` | rw- | r-- | rw- | Write Earth & Intent; read Heaven |

This is the whole separation of powers, expressible in nine characters. The
Chancellor can write **only** to Intent — it literally cannot modify code. War
cannot touch Heaven — it cannot run tests. Forge cannot freely write Intent —
only append to it.

The court is not *trusting* its ministers. It is *handicapping* them.

Next: with the court understood, we install it and issue your first edict.
