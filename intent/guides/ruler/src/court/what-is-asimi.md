# What Is Asimi?

> *"The beginning of wisdom is to call things by their right names."*
> — attributed to Confucius (孔子)

Before we install anything, we must agree on what we are building, and why it
is shaped the way it is. Everything else in this book — every command, every
concept, every ritual — follows from a single design decision:

**Asimi is not an unopinionated agent. It is a court.**

## The problem with unopinionated agents

Most coding agents are one mind with one job: you type a request, a model
writes code, and you check the result. They are **unopinionated** — they impose
no structure on how the work is done, leave the division of labour to you, and
have no separation of powers of their own. This works until it doesn't. An
unopinionated agent can:

- write code that breaks a test it also wrote,
- declare success on a change nobody verified,
- drift from your intent and never tell you,
- and, worst of all, *guess* when it should have asked.

These are not model failures. They are **institutional** failures. Unstructured
work cannot check itself, and a system that trusts one actor trusts it
completely or not at all.

## The institutional answer

Asimi's answer is ancient and well-tested: **divide the work, separate the
powers, and require a seal at every gate.** Instead of one unopinionated agent, you get a
small government:

- A **Secretary** who talks to you and distills your intent into work orders.
- A **strategist** who plans how the work should be done.
- A **Forge** that shapes the code.
- A **Judge** that weighs the result against evidence.
- A **Chancellor** who reviews against the law of the codebase.

No single minister can both write the change and approve it. If the Forge fakes
a test, the Judge catches it. If the Judge misses something, the Chancellor's
review catches it. If all three agree but the work has drifted from your intent,
your **seal** — the final gate — is still yours to grant or withhold.

## Why this works

Two thousand years ago, in a different domain, the same problem appeared: how
do you run a large enterprise without trusting any single official with
absolute power? The imperial Chinese answer — refined across dynasties — was
*checks within the bureaucracy itself*. Asimi borrows that answer wholesale.

The result is a system with an unusual property for an AI tool: **it is
accountable.** Every change is proposed by one minister, verified by another,
reviewed by a third, and authorized by you. The record of all of it is written
down.

Throughout this book you will see three ideas recur — the **realms**, the
**seal chain**, and **rituals**. They are not decoration. They are the machine.
The next three chapters teach each of them in turn, from the ground up.
