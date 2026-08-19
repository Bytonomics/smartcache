---
type: Reference
title: "smartcache — Deferred: Bloom-Filter Alias Routing (Future Optimization)"
---

# smartcache — Deferred: Bloom-Filter Alias Routing (Future Optimization)

**Status:** Deferred. Not built. Captured so the approach and its hazard are not re-derived later.
**Relates to:** [`1-alias-cache-design.md`](./1-alias-cache-design.md), Section 9.

This document records a performance optimization for alias-based caching that was designed
and then deferred. It exists so a future implementer starts from the decision, not a blank page.

---

## 1. The problem it solves

An alias-group cache (created via `RegisterAliasGroup`) routes **all** of its operations
through the Lua path, even for
plain primary keys that belong to no group. That is correct but wasteful: a large fraction of
keys in an aliasing cache may never carry an alias, yet each pays for the grouped path.

**Goal:** let a key that is *definitely not grouped* skip the Lua path and use the light path
(`GET`/`SET`/`DEL`), while grouped keys still go through Lua.

---

## 2. The approach

Keep a single **in-memory bloom filter** per process, shared across all groups, holding every
group-participating key (each primary key and each alias) with scoped/prefixed key strings.

Routing per operation:

- Bloom says **"not present"** → the key is *definitely* not grouped → **light path**.
- Bloom says **"present"** → the key is *maybe* grouped → **Lua path**.

A bloom filter never gives a false negative *for elements added to it*. So within one process
that registered every alias, a "not present" answer is certain, and routing is exact.

**Correctness invariant (must hold if this is ever built):** the bloom may only ever cause a
*false positive relative to routing-to-Lua* (a non-grouped key sent to the Lua path, which
handles it gracefully — a wasted lookup, never a wrong result). It must **never** cause a
grouped key to be routed to the light path. See the hazard in Section 4.

---

## 3. Memory cost (the math that made this attractive)

Bloom size depends only on element count `n` and target false-positive rate `p`, not on key or
value size: `bits = -n · ln(p) / (ln 2)²`.

| Target FP rate `p` | Bits / element | Bytes / element | Hashes k |
|---|---|---|---|
| 1% | 9.6 | ~1.2 | 7 |
| 0.1% | 14.4 | ~1.8 | 10 |
| 0.01% | 19.2 | ~2.4 | 13 |

Only group-participating keys go in the filter, not all Redis keys. For a few GB of Redis data
(~1 KB records ⇒ ~3M records, ~2 aliases each ⇒ ~9M elements):

| Elements | @1% | @0.1% | @0.01% |
|---|---|---|---|
| 9M | 10.8 MB | 16.2 MB | 21.6 MB |
| 20M | 24 MB | 36 MB | 48 MB |
| 50M | 60 MB | 90 MB | 120 MB |

**Conclusion:** ~15–35 MB of process RAM at 0.1% FP for a few GB of Redis — under 1% overhead.
The cost was never the blocker. The correctness hazard below is.

---

## 4. The hazard that deferred it — cross-instance false negatives

An in-memory bloom is **per process**. In a multi-instance deployment, instance A's filter never
saw an alias that instance B registered. From A's view that alias is "not present" — a **false
negative relative to grouping**, the one thing the invariant in Section 2 forbids. A would route
a genuinely grouped key to the light path.

With the pointer-indirection key model (`1-alias-cache-design.md` Section 2), the *consequence* of
that misroute is bounded: it degrades to TTL-bounded staleness (a missed cascade heals when the
value key's TTL expires), never data corruption. That fits the library's "bounded staleness,
never permanent" north star — but it means a naive in-memory filter is **not a safe correctness
gate on its own** across instances. It is only exact in a **single-process** deployment (e.g. the
mems4 build), where it is perfectly safe and needs none of the machinery below.

### Warming strategies considered (pick one if this is built for multi-instance)

| Strategy | How | Trade-off |
|---|---|---|
| Rebuild on startup + accept TTL staleness | Seed the filter from existing pointer keys at boot; add local writes thereafter | No per-op Redis cost; a brand-new alias from another instance is a false negative until the next rebuild (TTL-bounded). |
| Redis pub/sub broadcast | Each `PutAliased` publishes the new key; all instances add it to their local filter within ms | Near-real-time warming, no per-read cost; adds a pub/sub dependency + subscriber goroutine; a missed message falls back to startup rebuild. |
| Redis-backed shared bloom (RedisBloom `BF.EXISTS`) | One authoritative filter in Redis, queried per routing decision | Always correct cross-instance, but adds a Redis round trip to the very ops the filter meant to keep local — defeats most of the win; needs the RedisBloom module. |

---

## 5. Deletion / false-positive growth — cuckoo-filter recommendation

A plain bloom filter cannot remove entries, so an evicted alias lingers as a permanent false
positive. That is **safe** (it only causes a wasted Lua lookup that finds nothing, then falls
back), but the false-positive rate slowly climbs as aliases churn.

Two ways to keep the FP rate flat:

- **Plain bloom + periodic rebuild** — simplest, lowest memory; rebuild from live pointer keys
  to reset the FP rate. FP is safe by construction, so rebuild cadence is a tuning knob, not a
  correctness requirement.
- **Cuckoo filter (recommended if immediate deletion is wanted)** — supports delete, so an
  evict removes its entries at once and the FP rate stays flat with no rebuild. Similar memory
  footprint to a bloom; a counting bloom filter also supports delete but at ~4× memory, so the
  cuckoo filter is the better deletable option.

---

## 6. When to revisit

Build this only when profiling shows the Lua path on non-grouped keys in an aliasing cache is a
real cost. Until then, an aliasing cache routes everything through the (correct) Lua path, and a
single-process build could adopt the exact in-memory filter with no warming machinery at all.
