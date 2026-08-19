---
type: Reference
title: "smartcache — Alias-Based Multi-Key Caching (Architecture Design)"
---

# smartcache — Alias-Based Multi-Key Caching (Architecture Design)

**Status:** Design approved — ready for implementation spec.
**Audience:** engineers building or maintaining `smartcache`.
**Module:** `github.com/Bytonomics/smartcache` (`smritea-oss/modules/golang/smartcache`).

This document is written as a cascade: it states a problem, gives the solution, then
states the **next** problem that solution creates, and solves that in turn — until the
design closes. Every decision carries a worked example so there is no room for a wrong
reading. A companion document, [`future-bloom-filter-routing.md`](./future-bloom-filter-routing.md),
holds one deferred optimization referenced near the end.

---

## 0. Vocabulary

Fixed terms. Each term has one meaning. The same term is used for the same thing everywhere.


| Term                 | Meaning                                                                                                         |
| -------------------- | --------------------------------------------------------------------------------------------------------------- |
| **entity**           | A registered `Cache[T]` — one type, one namespace. Example: the `user` cache.                                   |
| **namespace** (`ns`) | The entity's resolved key prefix: its `Prefix` override, or the cache name. Example: `user`.                    |
| **primary key**      | The main identity of one cached record. Example: `5` (a user id).                                               |
| **value key**        | The Redis key that holds the actual serialized value. Example: `bc:{user}:5`.                                     |
| **alias**            | A secondary lookup key for the same record. Example: `email:foo@bar.com`.                                       |
| **pointer**          | A small Redis key that maps one alias to a value key. Example: `bc:grp:{user}:email:foo@bar.com` → `bc:{user}:5`. |
| **members set**      | A Redis SET listing every pointer that belongs to one primary key. Example: `bc:memb:{user}:5`.                 |
| **group**            | One primary key plus every alias that points at it. Exactly one group per cache, by construction.               |
| **hash-tag**         | The `{...}` substring Redis Cluster uses to pick the slot. Here its content is the literal `ns` (e.g. `{user}`) — the **same for every record of the entity**, not a per-record id. |

**Read `{user}` literally.** Throughout this document `{user}` is the actual key text for the
`user` entity — every user record (`5`, `99`, `1000`) uses the identical hash-tag `{user}`. It
is *not* a placeholder for a user id. That sameness is what co-locates the whole entity on one
cluster slot (Section 6).


---

## 1. Problem — callers carry multi-key bookkeeping by hand

A cached record is often reachable by more than one key. Concretely, a **user** record is
looked up three ways:

- by **user-id** — `user:5`
- by **email** — `user:email:foo@bar.com`
- by **slug** — `user:slug:ada`

Today, to cache that user, a caller of `smartcache` must:

- write the value under **all three** keys it may be read by, and
- on any change to the user, evict **all three** keys itself.

Example of the burden: a background job updates user 5's name. The job must remember to evict
`user:5` **and** `user:email:foo@bar.com` **and** `user:slug:ada`. If it evicts only `user:5`
and forgets the email key, then a later login-by-email keeps serving the **old name** until
that key's TTL runs out. Every caller repeats this bookkeeping, and forgetting one key is a
silent staleness bug.

### Solution — the library owns the bookkeeping

`smartcache` associates **many lookup keys with one value**. A delete or update through
**any** key in the group cascades to **all** keys in the group. The caller stops tracking
the key set. The library tracks it, cascades it, and cleans it up.

This is symmetric: whether the caller acts through the user-id, the email, or the slug, the
whole group is affected the same way. Evicting user 5 by email also invalidates the id and
slug views; updating by slug is seen by a later read via email.

---

## 2. Problem — Redis has no multi-key → single-value primitive

Redis is a flat key-value store. Every key (STRING, SET, HASH, …) owns its own value.
There is no native "these N keys share one value, delete one and the rest die" feature.
Duplicating the value under N keys would force N separate writes and N separate deletes,
with no link between them — exactly the burden we are trying to remove.

### Solution — pointer indirection with a single value copy

Store the value **once**, at the value key. Every alias is a small **pointer** whose value
is the *value key it resolves to* — not a copy of the data.

```
bc:{user}:5                          -> {"ID":"5","Name":"Ada",...}      (the value, stored once)
bc:grp:{user}:email:foo@bar.com    -> bc:{user}:5                        (pointer)
bc:grp:{user}:slug:ada             -> bc:{user}:5                        (pointer)
```

A read through an alias is a two-step chain: read the pointer to learn the value key, then
read the value key. Because the value exists once, **deleting the value key invalidates
every alias at the same instant** — each alias then resolves to a value key that is gone,
which the reader treats as a miss and reloads.

**Decision (corrected from an earlier draft):** the pointer stores the **full value key**
(`bc:{user}:5`), not a bare primary (`5`). The resolver never rebuilds the value key from a
primary — it reads the value key verbatim from the pointer.

---

## 3. Problem — symmetric cascade needs both directions

Solution 2 lets an alias find its value (alias → value key). But an `Evict` through the
**primary** must find and remove **every alias**, and a "no leaks" rule means those pointer
keys must be actively deleted, not left to expire. That is the reverse direction
(primary → all its aliases), which the pointer alone cannot answer. Redis cannot list "all
keys whose value is `bc:{user}:5`" without a `SCAN`, and `SCAN`/`KEYS` are unsafe in a hot path.

### Solution — a members set as the inverse index

Keep a second index: a SET per primary that lists every pointer key in the group.

```
bc:memb:{user}:5  ->  { bc:grp:{user}:email:foo@bar.com , bc:grp:{user}:slug:ada }
```

There are now exactly **two** indexes, in **opposite directions**, with distinct jobs:


| Structure | Redis type | Direction          | Drives                | Required?                                       |
| --------- | ---------- | ------------------ | --------------------- | ----------------------------------------------- |
| Value     | STRING     | —                  | the actual read/write | Yes — holds the data, once                      |
| Pointer   | STRING     | alias → value key  | **alias lookup**      | Yes — an alias cannot find its value without it |
| Members   | SET        | primary → pointers | **cascade cleanup**   | Yes, given the no-leak rule                     |


**The members set does not drive lookup** — the pointer does. The members set is read only
in reverse, to enumerate pointers for deletion on evict or reassignment. It is optional
only if one accepts leaked pointers (rejected: violates no-leak) or a `SCAN` (rejected:
unsafe). Given the no-leak requirement, it is required.

---

## 4. Problem — this must be correct across many server instances

The control-plane / data-plane deployment runs **many instances** of a service, possibly on
different continents, sharing one Redis. If instance A evicts a record through the id, and
instance B later reads it through the email, B must see the eviction. In-memory state on A
is invisible to B.

### Solution — all state lives in shared Redis; no in-memory authority

The value, the pointers, and the members set all live in the shared Redis. No instance
holds authoritative group state in memory. The cross-instance cascade then works with no
gossip and no push:

1. Instance A evicts through the id. It deletes the shared value key `bc:{user}:5`.
2. That single delete is the atomic invalidation point — the value is gone for everyone.
3. Instance B reads through the email. It reads the pointer (shared), gets `bc:{user}:5`,
 reads it, finds it gone, treats the read as a miss, and reloads.

The lingering pointer keys are harmless dangling references, cleaned by the members-set
Lua (Section 6) and backstopped by TTL. This is the "fail-safe direction" of inconsistency:
leftover garbage that self-heals, never a missing value that corrupts a read.

---

## 5. Problem — the cascade is read-then-branch-then-delete-many, atomically

An `Evict` through an alias must: read the pointer, branch on what it finds, read the
members set, then delete many keys. Redis `MULTI`/`EXEC` **queues** commands and cannot
read a value mid-transaction and branch on it; making it correct needs `WATCH` plus a retry
loop that is expensive under concurrency.

### Solution — a Lua script, behind an optional store interface

The grouped path runs a **Lua script / Redis Function**: Redis executes it atomically on
the server, so it can read, branch, and delete many keys in one round trip with no retry
loop. The script is also written to handle a non-grouped key gracefully (it simply finds no
group and does nothing harmful).

`Cache[T]` never writes Lua itself. A new **optional** store interface carries the grouped
operations, exactly mirroring the existing `BatchCacheStore` precedent (detected once at
`Register` via a comma-ok type assertion, stored as a nilable field):

```go
// AliasCacheStore is an optional CacheStore extension for backends that can
// track atomic key groups. redisstore implements it with a Lua script;
// memstore implements it with its existing mutex (single-process, exact).
// Method names deliberately mirror CacheStore's own verb style (Get.../Put.../Evict...)
// rather than a separate "Grouped"/"Resolve" vocabulary.
type AliasCacheStore interface {
	CacheStore

	// GetByAlias resolves a pointer key to its value key, then reads the value.
	// Returns ErrStoreMiss when either link is absent.
	GetByAlias(ctx context.Context, pointerKey string) ([]byte, error)

	// PutByAlias writes the value at its value key and, when a pointer is given,
	// creates/refreshes that alias pointer (one-alias-per-field replacement, plus
	// cross-primary steal cleanup) and adds it to the primary's members set —
	// every touched key sharing the ONE ttl. The whole write is one atomic Lua on
	// the {ns} slot (Section 6).
	PutByAlias(ctx context.Context, spec *AliasWriteSpec) error

	// EvictByPrimary deletes the value key, every pointer in the primary's
	// members set, and the members set itself — one atomic Lua on the {ns} slot.
	EvictByPrimary(ctx context.Context, valueKey, membersKey string) error

	// EvictByAlias resolves the pointer to its primary, then cascades exactly
	// like EvictByPrimary.
	EvictByAlias(ctx context.Context, pointerKey, valueKeyPrefix, membersKeyPrefix string) error
}

// AliasWriteSpec carries the pre-built key strings (from keyspace.go) for one grouped
// write. The pointer-related fields are empty for a primary-only value write, which
// still refreshes every existing group key's TTL.
type AliasWriteSpec struct {
	ValueKey         string        // bc:{ns}:<primary>
	MembersKey       string        // bc:memb:{ns}:<primary>
	PointerKey       string        // bc:grp:{ns}:<field>:<value>   ("" => primary-only write)
	FieldPrefix      string        // bc:grp:{ns}:<field>:          ("" => primary-only write)
	ValueKeyPrefix   string        // bc:{ns}:      (steal-cleanup: parse old primary from old value key)
	MembersKeyPrefix string        // bc:memb:{ns}: (steal-cleanup: rebuild old primary's members key)
	Value            []byte
	TTL              time.Duration // the single jittered TTL for EVERY key in the write
}

type AliasRef struct{ Field, Value string } // e.g. {"email","foo@bar.com"}

// PrimaryKeyed is implemented by the cached value type T (or *T) on an alias-group
// cache. It lets GetByAlias's read-through learn a freshly-loaded value's primary key
// on a miss, so it can rebuild the group (Section 11).
type PrimaryKeyed interface{ CachePrimaryKey() string }
```

The interface is high-level on purpose: the backend owns the whole grouped operation as one
same-slot Lua (Section 6). `Cache[T]` stays backend-agnostic, just as it is today — it only
ever sees `CacheStore`/`AliasCacheStore`, never a Redis client.

---

## 6. Problem — atomic Lua needs same-slot, so the value key must co-locate too

On Redis Cluster, a multi-key Lua script requires every key it touches to hash to the same
slot. The pointer keys (`bc:grp:{user}:...`) and the members set (`bc:memb:{user}:5`) already
carry the `{user}` hash-tag, so they share one slot. But the value key as first drafted —
`bc:{user}:5`, with no hash-tag — lands on a **different** slot. A single Lua script therefore
could **not** touch the value key together with the pointers and members. The earlier draft
worked around this by deleting the value key as a separate client-side step outside the Lua —
which is two operations with a crash window, not one atomic operation.

### Solution — co-locate the whole entity under `{ns}`; one atomic Lua

The hash-tag content is the **entity namespace** — the literal cache name (`user`), known at
registration. It is not a groupID we must discover at lookup time, and there is exactly one
group per cache (Section 8), so the same tag `{user}` applies to **every** key of the entity —
including the value key: `bc:{user}:5`. Now the value, the pointers, and the members set all
share the `{user}` slot, and **one Lua script performs the entire grouped operation
atomically** — write/delete value + create/delete pointers + update members, in a single
server-side step.

**The trade-off (accepted):** co-locating by `{ns}` pins **all** of an aliasing entity's cached
values — the real data, not just tiny pointers — onto one cluster slot, i.e. one node. That
entity loses horizontal spread across the cluster. This is the deliberate price of atomic
cascades, and it is why aliasing is opt-in and why, for now, the Lua path is the only path for
grouped entities. **Non-aliasing caches must NOT hash-tag their value keys** — they keep the
plain `bc:<ns>:<primary>` form so their values distribute normally.

The resulting key hierarchy — **defined in a single constants file** (`keyspace.go`), the only
place any key string is assembled:

| Purpose | Key format (aliasing cache) | Stored value | Hash-tag |
| ------- | --------------------------- | ------------ | -------- |
| **Value** | `bc:{<ns>}:<primary>` | serialized `T` | `{<ns>}` |
| **Pointer** | `bc:grp:{<ns>}:<field>:<value>` | the value key `bc:{<ns>}:<primary>` | `{<ns>}` |
| **Members** | `bc:memb:{<ns>}:<primary>` | SET of pointer keys | `{<ns>}` |

| Purpose | Key format (non-aliasing cache) | Hash-tag |
| ------- | ------------------------------- | -------- |
| **Value** | `bc:<ns>:<primary>` | none — values distribute across slots |

- `bc` = the global cache namespace (Bytonomics Cache).
- `grp` = pointer segment, `memb` = members segment (short, to save space).
- `{<ns>}` = the cluster hash-tag; its content is the literal entity namespace (e.g. `{user}`),
  identical for every record of the entity — not a per-record id.

**Worked example** (`ns = user`, primary `5`, aliases email + slug):

```
bc:{user}:5                        -> {"ID":"5","Name":"Ada"}
bc:grp:{user}:email:foo@bar.com    -> bc:{user}:5
bc:grp:{user}:slug:ada             -> bc:{user}:5
bc:memb:{user}:5                   -> { bc:grp:{user}:email:foo@bar.com , bc:grp:{user}:slug:ada }
```

All four keys contain `{user}`, so all four hash to the same slot — one atomic Lua can touch
every one of them.

**Worked example, custom prefix** (`ns = user_custom_name`): identical shape —
`bc:{user_custom_name}:5`, `bc:grp:{user_custom_name}:email:...`, `bc:memb:{user_custom_name}:5`.

An `EvictByPrimary` for primary `5` therefore runs as **one** Lua on the `{user}` slot: read
`bc:memb:{user}:5`, delete the value `bc:{user}:5`, delete every pointer the set lists, delete
the members set — atomically, no separate client-side step.

---

## 7. Problem — TTL jitter can desynchronize a group's expiry

`smartcache` applies downward-only TTL jitter so a batch of keys does not all expire at
once. If the value key, the pointers, and the members set were each jittered independently,
the earliest one could expire on its own — an alias could report a miss while the value and
its siblings still resolve. That is the exact inconsistency aliasing is meant to prevent.

### Solution — one jitter per grouped write, applied to every key

The jittered TTL is computed **once per grouped write** and reused, byte-identical, for the
value key, every pointer key, and the members set. It is **never** re-jittered per key. This
is a hard rule, carried in `AliasWriteSpec.TTL` and enforced inside the Lua write path.

---

## 8. Problem — how a cache opts into aliasing, and where the group name comes from

A cache must declare that it uses aliasing, so the majority of non-aliasing caches keep the
untouched fast path. And each alias group needs a namespace for its hash-tag. Two questions:
what selects aliasing, and what supplies the group name?

### Solution — a dedicated constructor; one group per cache; auto-derived name

**There is exactly one alias group per cache.** So the group namespace is not a parameter — it
is derived automatically from the cache name (`ns`). And aliasing is selected by **which
constructor you call**, not by a flag. Two constructors:

```go
// Register[T] — the existing constructor. A normal cache: light path only,
// un-tagged bc:<ns>:<primary> value keys, values distribute across the cluster.
// Unchanged from today.
func Register[T any](m *Manager, name string, opts *EntityOptions) (*Cache[T], error)

// RegisterAliasGroup[T] — the new constructor (the "NewAliasGroup" method), for an
// alias-group cache. It derives the group namespace from name, uses hash-tagged
// bc:{<ns>}:<primary> keys, routes operations through the Lua path, and exposes PutAliased.
func RegisterAliasGroup[T any](m *Manager, name string, opts *EntityOptions) (*Cache[T], error)
```

- `Register[T]` → a normal cache: `Get`/`Put`/`Evict` do a plain `GET`/`SET`/`DEL`, zero
  detection, byte-for-byte unchanged from today.
- `RegisterAliasGroup[T]` → an alias-group cache: its operations route through the alias-aware
  (Lua) path, and its keys are hash-tagged (Section 6).

There is **no** `EnableAliasing` flag on `EntityOptions` — the constructor choice is the opt-in.

**`RegisterAliasGroup` fails fast:** if the manager's injected store does **not** implement
`AliasCacheStore`, it **panics** — a misconfiguration should crash at initialization, not
silently at runtime. (This matches the library's existing constructor-panic discipline, e.g.
`ErrPointerType`.)

---

## 9. Problem — even an aliasing cache routes non-aliased keys through Lua

Within an alias-group cache, most keys may still be plain primary lookups with no
alias. Routing every one of them through the Lua path is correct but wasteful.

### Solution — deferred: an in-memory bloom filter (documented, not built now)

A future optimization adds an **in-memory bloom filter** that proves a key is *definitely
not* grouped, so it can skip the Lua path and use the light path. It is a **pure performance
optimization with no role in correctness** — the Lua path remains correct on its own and
handles non-grouped keys gracefully.

This is **deferred**. Until it lands, an alias-group cache sends all of its  
operations through the alias-aware (Lua) path. The full approach — the memory math, the  
cross-instance false-negative problem that makes a naive in-memory filter unsafe as a  
correctness gate, and the cuckoo-filter recommendation for deletability — is captured in  
[`2-future-bloom-filter-routing.md`](./2-future-bloom-filter-routing.md) and on the roadmap. We may decide to use cuckoo filter as well, to support deletion of keys from it.

---

## 10. Problem — reassigning a group's aliases could leak old pointers

An entity's alias set can change: a user's slug is updated, so `slug:ada` should stop
pointing at the record and `slug:ada2` should start. A naive re-write would create the new
pointer and orphan the old one, which then lingers and resolves to the record forever — a leak.

### Solution — diff-and-clean inside the Lua write

`PutByAlias` diffs the requested alias field against what the members set already holds (one
alias per field per primary — Decisions log D11a): re-registering a field replaces its old
pointer. It also handles cross-primary steal — if the alias already belonged to a different
primary, it is removed from that primary's members set — so a later evict there cannot delete
a pointer another record now owns. All of this happens inside the same atomic Lua on the
`{ns}` slot. No stale pointer survives.

Because aliases are added **one per call** (Section 11), the common case is "add one alias";
the diff matters most on an explicit re-registration that drops or reassigns a previously-added
alias.

---

## 11. Problem — forcing every writer to declare all aliases couples writers

If the only way to cache a record were a `PutAliased(primary, allAliases, writer)`  (rejected approach) that  
listed every alias, then every writer everywhere would have to know the full alias set of  
the entity — reintroducing the coupling this feature removes. And most aliases may never be  
exercised: which secondary keys a record is actually looked up by depends on the paths the  
application happens to take.

### Solution — lazy, one alias per call; explicit `AliasRef` methods for alias access

- **Alias creation is explicit and incremental.** Two new methods add **one** alias at a time,
as the application first needs it — one that runs a writer, one for a value already held:
  ```go
  // PutAliased writes (or refreshes) the value for primaryKey and registers ONE alias
  // that resolves to it. Call it again, per alias, as new lookup paths appear.
  func (c *Cache[T]) PutAliased(ctx context.Context, primaryKey string, alias AliasRef, writer Writer[T]) (*T, Outcome, error)

  // PutAliasedValue is PutAliased's cache-warming twin: the caller already holds the
  // value (e.g. just wrote it to the source of truth) and only wants the alias registered,
  // without re-running a writer.
  func (c *Cache[T]) PutAliasedValue(ctx context.Context, primaryKey string, alias AliasRef, val *T) error
  ```
- **Alias access is explicit, via `AliasRef`, on separate methods — not by overloading a
  single string key.** `Get`, `Put`, `PutValue`, and `Evict` keep taking the **primary key
  only**; a distinct `AliasRef`-typed method exists for the alias side of each:
  ```go
  func (c *Cache[T]) GetByAlias(ctx context.Context, alias AliasRef, loader Loader[T]) (*T, Outcome, error)
  func (c *Cache[T]) EvictByAlias(ctx context.Context, alias AliasRef) error
  ```
  This avoids parsing a single string key to decide "is this a primary or an alias" — a scheme
  that breaks if a primary or an alias value ever contains the field separator. Both directions
  still cascade the whole group: `EvictByAlias` deletes the value key and cleans every pointer +
  the members set, exactly as a primary `Evict` does; `Put`/`PutValue` on the primary update the
  one shared value, so every alias sees the new value on its next read.

- **`GetByAlias` is read-through, via `PrimaryKeyed`.** On a miss it runs `loader`, exactly like
  `Get` does — but unlike a primary miss (where the caller already supplied the primary key), an
  alias miss does not by itself reveal the primary key needed to store the value. So the loaded
  value's type must implement `PrimaryKeyed`:
  ```go
  type PrimaryKeyed interface{ CachePrimaryKey() string }
  ```
  `GetByAlias` reads `CachePrimaryKey()` off the freshly-loaded value, then writes the value and
  registers this alias for it — the same rebuild `PutAliased` would perform, done automatically
  on a cold alias read.

  There is therefore **no** `UpdateAliased` — a plain `Put`/`PutValue` on the primary already
  updates every alias's view. `PutAliased`/`PutAliasedValue`/`GetByAlias`/`EvictByAlias` are the
  four alias-specific additions; every other method on `Cache[T]` is unchanged.

---

## 12. Problem — does any of this break the single-process build?

`smartcache` must also serve a single-server / single-process deployment (the mems4 build),
where there is exactly one process and no cross-instance concern.

### Solution — single-process is the easy case, fully supported and exact

Every distributed concern in this document (Section 4, the deferred bloom filter's
false-negative risk) arises only from multiple processes sharing state. With one process:

- `memstore` implements `AliasCacheStore` with its **existing mutex** guarding a second map —
atomic within the process, consistent with everything `memstore` already promises.
- There is no cross-instance visibility problem, because there is only one instance.

So the same `Cache[T]` code path serves both builds unchanged. The single-process build is
strictly simpler, and correct by construction.

---

## Data model summary (one place)

```
Aliasing cache:
  Value    : bc:{<ns>}:<primary>                     (STRING)  the value, once; {ns} hash-tag
  Pointer  : bc:grp:{<ns>}:<field>:<value>           (STRING)  -> value key; {ns} hash-tag
  Members  : bc:memb:{<ns>}:<primary>                (SET)     pointer keys; {ns} hash-tag

Non-aliasing cache:
  Value    : bc:<ns>:<primary>                       (STRING)  no hash-tag; distributes
```

Every key in a group shares one jittered TTL, computed once per write. All key strings are
built only by `keyspace.go`. The `{...}` content is the literal `ns` (e.g. `{user}`), identical
for every record of the entity.

---

## Operation flows (step by step, with the running example)

Assume `ns = user`, an alias-group cache (created via `RegisterAliasGroup`).

**PutAliased("5", {email, foo@bar.com}, writer)**

1. Run `writer` → value; marshal once.
2. Compute the jittered TTL once.
3. `PutByAlias`: set `bc:{user}:5` = value; set `bc:grp:{user}:email:foo@bar.com` = `bc:{user}:5`;
 `SADD bc:memb:{user}:5` the pointer key; one-per-field replace / cross-primary steal cleanup
 as needed — all under the one TTL.

**GetByAlias({email, foo@bar.com})** (alias read)

1. `GetByAlias(bc:grp:{user}:email:foo@bar.com)` → resolves the pointer to `bc:{user}:5`, reads
 it → value (or either link gone → `ErrStoreMiss` → treated as a miss).
2. On a miss: run the caller's loader, read `CachePrimaryKey()` off the result, then
 `PutByAlias` to write the value and register this alias — the same rebuild `PutAliased`
 performs, done automatically.

**Get("5")** (primary read)

1. `GET bc:{user}:5` → value directly. No pointer step.

**Put("5", writer)** / **PutValue("5", val)** (update through the primary)

1. Resolve to the value key (`bc:{user}:5`); write via `PutByAlias` with no pointer (primary-only
 write), which still refreshes every existing group key's TTL.
2. Every alias now resolves to the new value on its next read.

**Evict("5")** (delete through the primary)

1. One atomic Lua on `{user}`: read `bc:memb:{user}:5`, `DEL bc:{user}:5` (the invalidation),
 delete every pointer the set lists, delete the members set. Nothing leaks.

**EvictByAlias({email, foo@bar.com})** (delete through an alias — symmetric result, explicit call)

1. One atomic Lua on `{user}`: resolve the pointer to primary `5`, then cascade exactly as
 `Evict("5")` does. Nothing leaks.

---

## Decisions log


| #   | Decision                                                                            | Rationale                                                                                                   |
| --- | ----------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| D1  | Pointer indirection, single value copy (not value duplication)                      | One value key → deleting it invalidates all aliases atomically, even on cluster.                            |
| D2  | Pointer stores the full value key, not a bare primary                               | Resolver reads the value key verbatim; no reconstruction assumptions.                                       |
| D3  | Two indexes: pointer (lookup) + members set (cleanup)                               | Redis cannot answer primary→aliases without a SCAN; the set is the inverse index.                           |
| D4  | Lua / Redis Function for the grouped path                                           | Only Lua does read-branch-delete-many atomically without a WATCH retry loop.                                |
| D5  | `AliasCacheStore` optional interface, comma-ok at `RegisterAliasGroup`; method names (`GetByAlias`/`PutByAlias`/`EvictByPrimary`/`EvictByAlias`) mirror `CacheStore`'s own verb style | Mirrors the shipped `BatchCacheStore` precedent; keeps `Cache[T]` backend-agnostic; avoids a separate "Grouped"/"Resolve" naming vocabulary. |
| D6  | Hash-tag `{ns}` on **all** keys of an aliasing entity — value, pointer, members     | Co-locates the whole group on one slot → the entire grouped op is one atomic Lua. Price: that entity's values are pinned to one cluster node (no horizontal spread). Non-aliasing caches keep un-tagged, distributing value keys. |
| D7  | One jittered TTL per grouped write, applied to every key                            | Prevents desynchronized expiry within a group.                                                              |
| D8  | Opt-in by dedicated constructor `RegisterAliasGroup` (not a flag); one group per cache; group namespace auto-derived from the cache name; panics if store lacks `AliasCacheStore` OR `T` is not `PrimaryKeyed` | Constructor choice is the opt-in; non-aliasing caches keep the untouched fast path; misconfig crashes at init, not at first alias read. |
| D9  | Bloom filter deferred; pure optimization, no correctness role                       | Lua path is correct alone and handles non-grouped keys gracefully.                                          |
| D10 | Diff-and-clean stale pointers inside the Lua write                                  | Satisfies the "no leaks / clean all metadata" requirement on reassignment.                                  |
| D11 | One alias per `PutAliased`/`PutAliasedValue` call; lazy; `PutAliasedValue` is the value-held twin of `PutAliased` (no writer re-run) | Writers don't need to know the full alias set; aliases appear as paths do; avoids a redundant load when the caller already holds the value. |
| D11a | One alias per field per primary — re-registering a field replaces its old pointer (not additive) | Matches single-valued identity fields (one email, one slug); gives `PutByAlias` a well-defined diff to clean on reassignment (Section 10). |
| D12 | Alias access is **explicit**, via `AliasRef`-typed methods (`GetByAlias`, `EvictByAlias`) — not by overloading `Get`/`Evict` to accept "any key, primary or alias" | A single string key can't safely distinguish primary from alias if either value contains the field separator; explicit methods have no such ambiguity. `Put`/`PutValue`/`Evict` keep taking only the primary key; there is no `UpdateAliased` because a primary `Put`/`PutValue` already updates every alias's view. |
| D12a | `GetByAlias` is read-through via `PrimaryKeyed`: on a miss it runs the loader, reads `CachePrimaryKey()` off the result, and rebuilds the group | Requested explicitly so alias reads get the same "miss → load → cache" ergonomics as `Get`, without the caller needing to already know the primary key. |
| D13 | Per-entity group namespace (`{ns}`) → keys globally unique                          | No cross-entity alias collision; the merge/steal problem does not arise.                                    |
| D14 | All key strings built only in `keyspace.go`                                         | Single source of truth for the keyspace, per repo Redis-key-centralization rule.                            |
| D15 | Single-process (mems4) fully supported via `memstore` mutex                         | No cross-instance concern; strictly the easy case.                                                          |


---

## Testing decisions

A good test asserts **external behavior** through the highest existing seam, not internal
mechanics. The highest seam here is `Cache[T]`'s public methods driven over a store fake —
the same seam the current `cache_test.go` uses.

- **`Cache[T]` behavior** (over `memstore` extended with `AliasCacheStore`): `PutAliased` write →
`Get` (primary) and `GetByAlias` (each field) both return the value; `Evict` (primary) and
`EvictByAlias` both invalidate the whole group; `Put`/`PutValue` on the primary is seen through
every alias; one-per-field replacement and cross-primary steal cleanup leak nothing;
`GetByAlias` read-through rebuilds the group via `PrimaryKeyed` on a miss; one shared TTL across
value/pointer/members; caches created via `Register` (non-aliasing) behave byte-for-byte as
today (regression guard).
- **`redisstore` `AliasCacheStore`** (over a fake `RedisConn` extended with the Lua-eval and
set methods): the Lua write/evict issues the expected same-slot operations; `GetByAlias` maps
a pointer to its value key; a missing pointer returns `ErrStoreMiss`.
- **`RegisterAliasGroup`**: with a non-`AliasCacheStore` store it panics; with a `T` that is not
`PrimaryKeyed` it panics; with a valid store and `PrimaryKeyed` `T` it succeeds.
- **Prior art:** `cache_test.go`, `getmany_test.go`, `redisstore_test.go`, `jitter_test.go`.

Unit tests only — no live Redis (fake `RedisConn`), consistent with the module's existing
test strategy.

---

## Out of scope

- The bloom-filter routing optimization (Section 9) — deferred; see companion doc + roadmap.
- Cross-instance stampede protection and two-level caching — separate roadmap items.
- Any cloud `Store` adapter, RBAC/authz re-basing, or consumer wiring in `smritea-cloud`.
- Redis Cluster hot-slot mitigation for a very large single entity (see open note below).

---

## Open implementation notes (carry into the spec, not blocking)

1. **Per-entity slot concentration.** `{ns}` co-locates one aliasing entity's *entire* keyspace —
 values, pointers, and members — on one cluster slot, i.e. one node. Unlike a bookkeeping-only
 variant, this includes the real value data, so a very large aliasing entity has no horizontal
 spread across the cluster and concentrates its read/write traffic on one node. Accepted as the
 price of atomic cascades; the spec must call it out, and it is why aliasing is opt-in.
2. **Lua dynamic keys.** Pointer names come from `SMEMBERS` at run time, not pre-declared in
 `KEYS[]`. They all share the `{ns}` slot, so the same-slot rule holds; the implementer must
 verify the target Redis version's rule on same-slot-but-undeclared key access in scripts.

