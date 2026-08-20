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
reading. A companion document, [`2-future-bloom-filter-routing.md`](./2-future-bloom-filter-routing.md),
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
| **pointer**          | A small Redis key that maps one alias to its owning primary key (`AliasSharded`) or its value key (`AliasColocated`). Example: `bc:grp:{user}:email:foo@bar.com` → `5`. |
| **members**          | A Redis HASH, one per primary key, mapping `field → aliasValue` for every alias currently registered on that primary. Example: `bc:memb:{user}:5` → `{email: foo@bar.com, slug: ada}`. |
| **group**            | One primary key plus every alias that points at it. Exactly one group per cache, by construction.               |
| **hash-tag**         | The `{...}` substring Redis Cluster uses to pick the slot. Its content depends on the cache's `AliasMode` — see [Slot-placement strategies](#7-problem--co-locating-the-whole-entity-creates-a-cluster-hotspot). |
| **slot-placement strategy** | The per-cache `AliasMode` choice — `AliasColocated` (default) or `AliasSharded` — selecting how a group's keys are distributed across Redis Cluster slots. Introduced in [Slot-placement strategies](#7-problem--co-locating-the-whole-entity-creates-a-cluster-hotspot). |

**Read `{user}` literally.** Sections 1–6 below build the design assuming `AliasColocated`'s
hash-tag: `{user}` is the actual key text for the `user` entity — every user record (`5`, `99`,
`1000`) uses the identical hash-tag `{user}`, not a placeholder for a user id. That sameness is
what co-locates the whole entity on one cluster slot (Section 6). Section 7 shows why that
sameness is also a cluster hotspot, and introduces `AliasSharded` as the alternative, whose
hash-tags are per-record (`{user:5}`) and per-alias (`{user:email:foo@bar.com}`) instead.


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
is the *primary key it resolves to* — not a copy of the data.

```
bc:{user}:5                          -> {"ID":"5","Name":"Ada",...}      (the value, stored once)
bc:grp:{user}:email:foo@bar.com    -> 5                                  (pointer -> primary key)
bc:grp:{user}:slug:ada             -> 5                                  (pointer -> primary key)
```

A read through an alias is a two-step chain: read the pointer to learn the primary key, then
read the value key rebuilt from it. Because the value exists once, **deleting the value key
invalidates every alias at the same instant** — each alias then resolves to a value key that is
gone, which the reader treats as a miss and reloads.

**Decision:** the pointer stores the **bare primary key** (`5`), not the full value key
(`bc:{user}:5`). The resolver reads the primary from the pointer, then rebuilds the value key
from it. This holds in both slot-placement modes (Section 7).

---

## 3. Problem — symmetric cascade needs both directions

Solution 2 lets an alias find its value (alias → primary → value). But an `EvictByKey` through the
**primary** must find and remove **every alias**, and a "no leaks" rule means those pointer
keys must be actively deleted, not left to expire. That is the reverse direction
(primary → all its aliases), which the pointer alone cannot answer. Redis cannot list "all
keys whose value is `bc:{user}:5`" without a `SCAN`, and `SCAN`/`KEYS` are unsafe in a hot path.

### Solution — a members index as the inverse index

Keep a second index: one HASH per primary, mapping each registered field to its current alias
value.

```
bc:memb:{user}:5  ->  { email: foo@bar.com, slug: ada }
```

There are now exactly **two** indexes, in **opposite directions**, with distinct jobs:


| Structure | Redis type | Direction          | Drives                | Required?                                       |
| --------- | ---------- | ------------------ | --------------------- | ----------------------------------------------- |
| Value     | STRING     | —                  | the actual read/write | Yes — holds the data, once                      |
| Pointer   | STRING     | alias → value key  | **alias lookup**      | Yes — an alias cannot find its value without it |
| Members   | HASH (`field → aliasValue`) | primary → registered aliases | **cascade cleanup + validate-on-read** | Yes, given the no-leak rule |

**Decision (settled after this section was first drafted):** the members index is a HASH, not a
SET of pointer keys. A SET can only answer "which pointer keys exist"; a HASH additionally
answers "what value is field X currently set to", which `AliasSharded`'s validate-on-read
(Section 7) depends on. Both slot-placement strategies use this same HASH shape.

**The members index does not drive lookup** — the pointer does. The members index is read only
in reverse, to enumerate pointers for deletion on evict or reassignment, and (for `AliasSharded`)
to validate a resolved pointer before trusting it. It is optional
only if one accepts leaked pointers (rejected: violates no-leak) or a `SCAN` (rejected:
unsafe). Given the no-leak requirement, it is required.

---

## 4. Problem — this must be correct across many server instances

The control-plane / data-plane deployment runs **many instances** of a service, possibly on
different continents, sharing one Redis. If instance A evicts a record through the id, and
instance B later reads it through the email, B must see the eviction. In-memory state on A
is invisible to B.

### Solution — all state lives in shared Redis; no in-memory authority

The value, the pointers, and the members index all live in the shared Redis. No instance
holds authoritative group state in memory. The cross-instance cascade then works with no
gossip and no push:

1. Instance A evicts through the id. It deletes the shared value key `bc:{user}:5`.
2. That single delete is the atomic invalidation point — the value is gone for everyone.
3. Instance B reads through the email. It reads the pointer (shared), gets `bc:{user}:5`,
 reads it, finds it gone, treats the read as a miss, and reloads.

The lingering pointer keys are harmless dangling references. Under `AliasColocated` they are
cleaned by the members-index Lua (Section 6); under `AliasSharded` they are cleaned by a
compare-and-delete pass, or expire via TTL (Section 7). This is the "fail-safe direction" of
inconsistency: leftover garbage that self-heals, never a missing value that corrupts a read.

---

## 5. Problem — the cascade is read-then-branch-then-delete-many, atomically

An `EvictByAlias` through an alias must: read the pointer, branch on what it finds, read the
members index, then delete many keys. Redis `MULTI`/`EXEC` **queues** commands and cannot
read a value mid-transaction and branch on it; making it correct needs `WATCH` plus a retry
loop that is expensive under concurrency.

### Solution — a Lua script, behind an optional store interface

The grouped path runs a **Lua script / Redis Function**: Redis executes it atomically on
the server, so it can read, branch, and delete many keys in one round trip with no retry
loop. The script is also written to handle a non-grouped key gracefully (it simply finds no
group and does nothing harmful).

`Cache[T]` never writes Lua itself. A new **optional** store interface carries the grouped
operations, exactly mirroring the existing `BatchCacheStore` precedent (detected once at
`RegisterAliasGroup` via a comma-ok type assertion). It is a **factory**, not a fixed set of
pre-built-key methods: given an entity namespace and a slot-placement mode (Section 7), it
returns a strategy handle that owns all key math and operation sequencing:

```go
// AliasRef names one secondary lookup key for an alias-group cache: a field ("email") and a
// value ("foo@bar.com").
type AliasRef struct {
	Field string
	Value string
}

// UniqueKeyed is implemented by the value type T (or *T) cached in an alias-group cache. It
// lets the library learn a value's primary key when rebuilding the group on a GetByAlias
// read-through miss (Section 12). It returns the primary key VALUE (e.g. "5").
type UniqueKeyed interface {
	CacheUniqueKey() string
}

// AliasMode selects the Redis-Cluster slot-placement strategy for an alias-group cache.
// See Section 7 for the full design of both.
type AliasMode int

const (
	AliasColocated AliasMode = iota // {ns} tag on every key; one atomic Lua per op. Default.
	AliasSharded                    // {ns:pk}/{ns:field:value} tags; distributed; validate-on-read.
)

// AliasOps is the per-(namespace, mode) strategy handle for an alias-group cache. It OWNS all
// key-math and operation sequencing; Cache[T] delegates to it with logical identifiers (a
// primary key or an AliasRef, never a pre-built key string) and keeps codec, the negative-marker
// convention, metrics, singleflight, and UniqueKeyed for itself. All methods are byte-level. A
// miss (absent, or a Sharded validate-on-read mismatch) is signalled by ErrStoreMiss.
type AliasOps interface {
	GetValue(ctx context.Context, primary string) ([]byte, error)
	PutValue(ctx context.Context, primary string, val []byte, ttl time.Duration) error
	EvictByPrimary(ctx context.Context, primary string) error
	GetByAlias(ctx context.Context, ref AliasRef) ([]byte, error)
	PutByAlias(ctx context.Context, primary string, ref AliasRef, val []byte, ttl time.Duration) error
	EvictByAlias(ctx context.Context, ref AliasRef) error
}

// AliasCacheStore is the optional CacheStore extension for backends that support alias groups.
// It is a FACTORY: given an entity namespace and slot mode it returns an AliasOps bound to them.
// RegisterAliasGroup calls AliasGroup once per cache. redisstore returns a Colocated or Sharded
// implementation (Section 7); memstore returns its single mode-agnostic implementation (one
// process has no cluster slots, so both modes behave identically there — Section 13).
type AliasCacheStore interface {
	CacheStore
	AliasGroup(ns string, mode AliasMode) AliasOps
}
```

The interface is high-level on purpose: whichever `AliasOps` a backend returns owns the whole
grouped operation for its mode (Section 6 for `AliasColocated`, Section 7 for `AliasSharded`).
`Cache[T]` stays backend-agnostic, just as it is today — it only ever sees
`CacheStore`/`AliasCacheStore`/`AliasOps`, never a Redis client.

---

## 6. Problem — atomic Lua needs same-slot, so the value key must co-locate too

On Redis Cluster, a multi-key Lua script requires every key it touches to hash to the same
slot. The pointer keys (`bc:grp:{user}:...`) and the members index (`bc:memb:{user}:5`) already
carry the `{user}` hash-tag, so they share one slot. But the value key as first drafted —
`bc:{user}:5`, with no hash-tag — lands on a **different** slot. A single Lua script therefore
could **not** touch the value key together with the pointers and members. The earlier draft
worked around this by deleting the value key as a separate client-side step outside the Lua —
which is two operations with a crash window, not one atomic operation.

### Solution — co-locate the whole entity under `{ns}`; one atomic Lua

The hash-tag content is the **entity namespace** — the literal cache name (`user`), known at
registration. It is not a groupID we must discover at lookup time, and there is exactly one
group per cache (Section 9), so the same tag `{user}` applies to **every** key of the entity —
including the value key: `bc:{user}:5`. Now the value, the pointers, and the members index all
share the `{user}` slot, and **one Lua script performs the entire grouped operation
atomically** — write/delete value + create/delete pointers + update members, in a single
server-side step.

**The trade-off (accepted):** co-locating by `{ns}` pins **all** of an aliasing entity's cached
values — the real data, not just tiny pointers — onto one cluster slot, i.e. one node. That
entity loses horizontal spread across the cluster. This is the deliberate price of atomic
cascades, and it is why aliasing is opt-in and why, for now, the Lua path is the only path for
grouped entities. **Non-aliasing caches must NOT hash-tag their value keys** — they keep the
plain `bc:<ns>:<primary>` form so their values distribute normally.

The resulting key hierarchy — **defined in a single constants file** (`internal/keyspace`), the
only place any key string is assembled. This table is `AliasColocated`'s key format; Section 7
gives `AliasSharded`'s different key format side by side with it:

| Purpose | Key format (aliasing cache, AliasColocated) | Stored value | Hash-tag |
| ------- | --------------------------- | ------------ | -------- |
| **Value** | `bc:{<ns>}:<primary>` | serialized `T` | `{<ns>}` |
| **Pointer** | `bc:grp:{<ns>}:<field>:<value>` | the value key `bc:{<ns>}:<primary>` | `{<ns>}` |
| **Members** | `bc:memb:{<ns>}:<primary>` | HASH: `field -> aliasValue` | `{<ns>}` |

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
bc:memb:{user}:5                   -> { email: foo@bar.com, slug: ada }
```

All four keys contain `{user}`, so all four hash to the same slot — one atomic Lua can touch
every one of them.

**Worked example, custom prefix** (`ns = user_custom_name`): identical shape —
`bc:{user_custom_name}:5`, `bc:grp:{user_custom_name}:email:...`, `bc:memb:{user_custom_name}:5`.

An `EvictByPrimary` for primary `5` therefore runs as **one** Lua on the `{user}` slot: `HGETALL`
`bc:memb:{user}:5`, delete the value `bc:{user}:5`, delete every pointer key rebuilt from that
HASH's `field -> value` pairs, delete the members index — atomically, no separate client-side
step.

This co-location is the complete design of the `AliasColocated` mode. Section 7 revisits the
trade-off it creates on Redis Cluster and introduces `AliasSharded` as the alternative.

---

## 7. Problem — co-locating the whole entity creates a cluster hotspot

Section 6's `{ns}` hash-tag pins **all** of an aliasing entity's keys — value, pointers,
members — onto one Cluster slot, i.e. one node. For a small or medium entity this is a fair
price for full atomicity. But for a **large or hot** entity (many records, high traffic), that
concentration becomes a genuine hotspot: every read and write for every record of that entity
lands on one node, no matter how many nodes the cluster has. Redis Cluster's own clustering
guidance treats a single low-cardinality hash-tag shared by many keys as an anti-pattern for
exactly this reason.

### Solution — a second strategy, `AliasSharded`, chosen per cache

Rather than replace Section 6's design, keep it as one of two selectable **slot-placement
strategies**, exposed as `AliasMode` (Section 5). Each cache picks one at `RegisterAliasGroup`
time via `EntityOptions.AliasMode` (Section 9); it does not change afterward.

- **`AliasColocated`** (Section 6's design, the default): `{ns}` tag on everything; one atomic
  Lua per operation; full atomicity; one slot per entity.
- **`AliasSharded`**: distributes value+members per record (`{ns:pk}` tag) and pointers per
  alias (`{ns:field:value}` tag); no single node carries the whole entity; correctness is kept
  by **validate-on-read** rather than full same-slot atomicity.

#### Strategy seam: factory delegation

`AliasCacheStore.AliasGroup(ns, mode)` (Section 5) returns the `AliasOps` handle bound to that
mode. `Cache[T]` holds that handle and delegates every grouped operation to it using logical
identifiers — it never constructs an alias key itself, in either mode. `redisstore` ships two
implementations, `colocatedOps` and `shardedOps`. `memstore` ships one mode-agnostic
implementation (Section 13) — a single process has no cluster slots, so placement strategy
cannot matter there. Key mathematics for both modes are shared in the module-internal
`internal/keyspace` package (Section 6, D14), so the two backends and both modes never diverge
on key format. Members are a Redis HASH (`field → aliasValue`, Section 3) in **both** modes; the
reverse pointer stores the **primary key** in both modes too — not a full value key, unlike
`AliasColocated`'s original pointer target (Section 2) — since `AliasSharded`'s value key lives
on a different slot than its pointer and must be rebuilt from the primary key on the record
side.

#### Key formats (both strategies)

| Key | `AliasColocated` | `AliasSharded` |
|-----|----------------|--------------|
| value | `bc:{ns}:<pk>` | `bc:{ns:pk}` |
| members (HASH field→value) | `bc:memb:{ns}:<pk>` | `bc:memb:{ns:pk}` |
| reverse pointer (→ pk) | `bc:grp:{ns}:<field>:<value>` | `bc:grp:{ns:field:value}` |

**Difference**: Colocated uses the `{ns}` hash-tag on all three structures, pinning them to one
slot. Sharded uses a slot-specific tag on the value + members (`{ns:pk}`, one slot per record),
and a separate slot for each reverse pointer (`{ns:field:value}`, one slot per alias).

#### Colocated algorithm: single slot, full atomicity

Every operation is **one atomic Lua script on the `{ns}` slot** (this is Section 6's design,
restated here for side-by-side comparison). Value, members HASH, and pointers all share the
`{ns}` hash-tag, so they land on the same cluster node.

- **One-per-field replace** (via `PutByAlias`): `HGET` the old alias value, `DEL` the old
  pointer, `SET` the new pointer, `HSET` the new field.
- **Cross-primary steal** (e.g., email moves from user 5 to user 99): `GET` the old pointer from
  `bc:grp:{ns}:email:...`, `HDEL` that field from the old primary's members HASH
  (`bc:memb:{ns}:5`).
- **TTL refresh** (on primary `PutByKey`/`PutValueByKey`): `PEXPIRE` the members HASH and every pointer key
  (fetched via `HGETALL` members).

All in one script, no race window.

#### Sharded algorithm: split atomicity, best-effort pointers

Value and members mutate atomically in a **record-slot Lua** (`{ns:pk}`); reverse pointers are
best-effort on their own **pointer-slot Lua or plain `SET`** (`{ns:field:value}`).

- **`PutByAlias`** (write/reassign): one record-slot Lua sets the value and `HSET`s the field in
  the members HASH, both atomically; then a **best-effort, unconditional** `SET` writes the new
  reverse pointer on its own slot. Unlike `AliasColocated`, this does **not** look up and delete
  a stale same-field pointer left behind by a prior value (e.g. the old slug's pointer) — doing
  so would need a second cross-slot round trip inside what is otherwise a single-slot atomic
  write, defeating the point of sharding. The stale pointer is harmless: it still resolves to
  the old primary key, but any read through it fails the `HGET members[field] == value` check
  below (the record's members HASH now holds the new value), so it reports a validated miss. It
  is removed lazily — by a later `EvictByPrimary`'s compare-and-delete pass on that primary, or
  by its own TTL — whichever happens first. (Verified directly against `shardedOps.PutByAlias`
  and `record_put.lua`: neither performs a lookup or delete of a prior alias value.)
- **`GetByAlias`** (2 hops): `GET` the reverse pointer on its own slot → learn the `pk`, then a
  record-slot Lua that returns the value **only if** `HGET members[field] == value`
  (validate-on-read). A stale or wrong pointer always resolves to a miss, never a wrong value.
- **`EvictByPrimary`** (3+ steps): a record-slot Lua deletes value + members and returns the
  members HASH contents. Then, **per member**, a compare-and-delete Lua on that alias's own
  slot: `if GET(pointer) == pk then DEL pointer`. This is best-effort — a concurrent steal on
  another pointer never wipes this record's pointer, because the delete is conditional on the
  primary-key match, and a per-pointer failure does not fail the whole evict.

Best-effort pointer writes plus validate-on-read make the strategy **self-healing**: a stale
reverse pointer always resolves to a validated miss (never a wrong read), so inconsistency is
always fail-safe — the same "fail-safe direction" property Section 4 establishes for
`AliasColocated`.

#### Trade-off summary and rule of thumb

| Aspect | `AliasColocated` | `AliasSharded` |
|--------|----------------|--------------|
| Slot granularity | Value + pointers + members on one `{ns}` slot | Value + members on `{ns:pk}` slot; pointers on `{ns:field:value}` slot |
| `GetByAlias` hops | Pointer + value (2 keys, 1 Lua) | Pointer + value + validation (2 keys, 2 Luas) |
| Write (add/update alias) | One Lua on `{ns}` | One Lua on `{ns:pk}` + one best-effort `SET` |
| Evict + cleanup | One Lua on `{ns}` — all keys gone atomically | Record Lua + per-pointer compare-delete (3+ ops) |
| Atomicity | Full — value + all pointers gone together | Partial — value + members atomic; pointers best-effort |
| Stale reads (wrong value) | Impossible — pointers deleted with value | Impossible — validate-on-read gate |
| Stale pointer on reassignment | Impossible — diff-and-clean inside the write Lua (Section 11) | Possible but harmless — self-heals via validate-on-read + lazy cleanup |
| Cluster hotspot | **Entity is pinned to one node** — all reads/writes stay on `{ns}` slot | Value + members scale per primary (`{ns:pk}`); pointer reads distributed by field+value |
| Best for | Single-instance Redis; small/medium entities on Cluster | Cluster deployments with large or hot entities |

**Rule of thumb**:
- **Single-instance** (or small entity): use `AliasColocated` for simplicity and full atomicity.
- **Redis Cluster with any hot or large aliased entity**: use `AliasSharded` to avoid the
  hotspot.
- **`AliasColocated` stays correct on Cluster** — it just concentrates all of an entity's
  traffic on one node. Choose `AliasSharded` only when that concentration is an actual problem.

---

## 8. Problem — TTL jitter can desynchronize a group's expiry

`smartcache` applies downward-only TTL jitter so a batch of keys does not all expire at
once. If the value key, the pointers, and the members index were each jittered independently,
the earliest one could expire on its own — an alias could report a miss while the value and
its siblings still resolve. That is the exact inconsistency aliasing is meant to prevent.

### Solution — one jitter per grouped write, applied to every key

The jittered TTL is computed **once per grouped write** and reused, byte-identical, for the
value key, every pointer key, and the members index. It is **never** re-jittered per key. This
is a hard rule, carried as the explicit `ttl time.Duration` parameter on every `AliasOps` write
method (`PutValue`, `PutByAlias`) (Section 5) — computed once by `Cache[T]` before it delegates,
then applied verbatim to every key that call touches: `AliasColocated`'s single Lua write, or
`AliasSharded`'s record-slot Lua plus its best-effort reverse-pointer write (Section 7).

---

## 9. Problem — how a cache opts into aliasing, and where the group name comes from

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

- `Register[T]` → a normal cache: `GetByKey`/`PutByKey`/`EvictByKey` do a plain `GET`/`SET`/`DEL`, zero
  detection, byte-for-byte unchanged from today.
- `RegisterAliasGroup[T]` → an alias-group cache: its operations route through the alias-aware
  (Lua) path, and its keys are hash-tagged (Section 6).

There is **no** `EnableAliasing` flag on `EntityOptions` — the constructor choice is the opt-in.

Within an alias-group cache, the **slot-placement strategy** (Section 7) is a separate, optional
choice: `EntityOptions.AliasMode *AliasMode`. When nil, `RegisterAliasGroup` defaults to
`AliasColocated`; passing `&AliasSharded` opts into the sharded design. This field is honored
only by `RegisterAliasGroup` — `Register`'s non-aliasing caches ignore it, since they have no
alias group to place. The chosen mode is fixed for the cache's lifetime; there is no runtime
switch or migration path between modes (see Out of scope).

**`RegisterAliasGroup` fails fast:** if the manager's injected store does **not** implement
`AliasCacheStore`, it **panics** — a misconfiguration should crash at initialization, not
silently at runtime. (This matches the library's existing constructor-panic discipline, e.g.
`ErrPointerType`.)

---

## 10. Problem — even an aliasing cache routes non-aliased keys through Lua

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

## 11. Problem — reassigning a group's aliases could leak old pointers

An entity's alias set can change: a user's slug is updated, so `slug:ada` should stop
pointing at the record and `slug:ada2` should start. A naive re-write would create the new
pointer and orphan the old one, which then lingers and resolves to the record forever — a leak.

### Solution — diff-and-clean inside the Lua write (AliasColocated); validate-on-read (AliasSharded)

Under `AliasColocated`, `PutByAlias` diffs the requested alias field against what the members
index already holds (one alias per field per primary — Decisions log D11a): re-registering a
field replaces its old pointer. It also handles cross-primary steal — if the alias already
belonged to a different primary, it is removed from that primary's members index — so a later
evict there cannot delete a pointer another record now owns. All of this happens inside the
same atomic Lua on the `{ns}` slot (Section 6). No stale pointer survives.

`AliasSharded` (Section 7) takes a different, weaker-but-safe approach: `PutByAlias` does **not**
proactively diff or delete a stale same-field pointer — doing so would need a second cross-slot
round trip inside what is otherwise a single-slot atomic write. Instead, correctness is kept by
validate-on-read: a stale pointer always resolves to a validated miss, never a wrong value, so
no reader ever observes a leak. The stale pointer itself is cleaned lazily — by a later
`EvictByPrimary`'s compare-and-delete pass on the old primary, or by its own TTL. Both modes
satisfy "no leak visible to a reader"; they differ only in whether cleanup is proactive
(Colocated) or lazy and self-healing (Sharded).

Because aliases are added **one per call** (Section 12), the common case is "add one alias";
the diff (or, for Sharded, the eventual lazy cleanup) matters most on an explicit
re-registration that drops or reassigns a previously-added alias.

---

## 12. Problem — forcing every writer to declare all aliases couples writers

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
  single string key.** `GetByKey`, `PutByKey`, `PutValueByKey`, and `EvictByKey` keep taking the
  **primary key only** (the value-derived `Put`/`PutValue`/`Evict` derive that key from
  `CacheUniqueKey()`); a distinct `AliasRef`-typed method exists for the alias side of each:
  ```go
  func (c *Cache[T]) GetByAlias(ctx context.Context, alias AliasRef, loader Loader[T]) (*T, Outcome, error)
  func (c *Cache[T]) EvictByAlias(ctx context.Context, alias AliasRef) error
  ```
  This avoids parsing a single string key to decide "is this a primary or an alias" — a scheme
  that breaks if a primary or an alias value ever contains the field separator. Both directions
  still cascade the whole group: `EvictByAlias` deletes the value key and cleans every pointer +
  the members index, exactly as a primary `EvictByKey` does; `PutByKey`/`PutValueByKey` on the primary
  update the one shared value, so every alias sees the new value on its next read.

- **`GetByAlias` is read-through, via `UniqueKeyed`.** On a miss it runs `loader`, exactly like
  `GetByKey` does — but unlike a primary miss (where the caller already supplied the primary key), an
  alias miss does not by itself reveal the primary key needed to store the value. So the loaded
  value's type must implement `UniqueKeyed`:
  ```go
  type UniqueKeyed interface{ CacheUniqueKey() string }
  ```
  `GetByAlias` reads `CacheUniqueKey()` off the freshly-loaded value, then writes the value and
  registers this alias for it — the same rebuild `PutAliased` would perform, done automatically
  on a cold alias read.

  There is therefore **no** `UpdateAliased` — a plain `PutByKey`/`PutValueByKey` on the primary already
  updates every alias's view. `PutAliased`/`PutAliasedValue`/`GetByAlias`/`EvictByAlias` are the
  four alias-specific additions; every other method on `Cache[T]` is unchanged.

---

## 13. Problem — does any of this break the single-process build?

`smartcache` must also serve a single-server / single-process deployment (the mems4 build),
where there is exactly one process and no cross-instance concern.

### Solution — single-process is the easy case, fully supported and exact

Every distributed concern in this document (Section 4; the deferred bloom filter's
false-negative risk, Section 10) arises only from multiple processes sharing state. With one
process:

- `memstore`'s `AliasGroup(ns, mode)` factory (Section 5) always returns the **same**
  mode-agnostic `AliasOps` implementation, regardless of which `AliasMode` the cache requested —
  a single process has no cluster slots for a placement strategy to distribute across, so the
  mode only affects the key strings `memstore` builds (still routed through `internal/keyspace`,
  Section 7), never its behavior.
- That implementation is guarded by `memstore`'s **existing mutex** — atomic within the process,
  consistent with everything `memstore` already promises. It always performs the full
  diff-and-clean + cross-primary-steal cleanup on `PutByAlias` (Section 11's `AliasColocated`
  behavior) and always validates on `GetByAlias` (Section 7's `AliasSharded` behavior) — there is
  no cross-slot cost to being both proactive and validating in a single process, so `memstore` is
  simply the strict combination of both strategies' safety properties.
- There is no cross-instance visibility problem, because there is only one instance.

So the same `Cache[T]` code path serves both builds unchanged. The single-process build is
strictly simpler, and correct by construction.

---

## Data model summary (one place)

```
Aliasing cache, AliasColocated (default):
  Value    : bc:{<ns>}:<primary>                     (STRING)  the value, once; {ns} hash-tag
  Pointer  : bc:grp:{<ns>}:<field>:<value>           (STRING)  -> value key; {ns} hash-tag
  Members  : bc:memb:{<ns>}:<primary>                (HASH)    field -> aliasValue; {ns} hash-tag

Aliasing cache, AliasSharded:
  Value    : bc:{<ns>:<primary>}                     (STRING)  the value, once; {ns:pk} hash-tag
  Pointer  : bc:grp:{<ns>:<field>:<value>}            (STRING)  -> primary key; {ns:field:value} hash-tag
  Members  : bc:memb:{<ns>:<primary>}                 (HASH)    field -> aliasValue; {ns:pk} hash-tag

Non-aliasing cache:
  Value    : bc:<ns>:<primary>                       (STRING)  no hash-tag; distributes
```

Every key in a group shares one jittered TTL, computed once per write. All key strings are
built only by `internal/keyspace` (Section 6, Section 7). The `{...}` content is the literal
`ns` for `AliasColocated` (e.g. `{user}`, identical for every record of the entity) or the
`ns:primary`/`ns:field:value` combination for `AliasSharded` (unique per record / per alias).
The one behavioral difference in what a pointer stores: `AliasColocated`'s pointer resolves to
the **value key**; `AliasSharded`'s pointer resolves to the bare **primary key**, since its
value key lives on a different slot and must be rebuilt from that primary key.

---

## Operation flows (step by step, with the running example)

Assume `ns = user`, an alias-group cache (created via `RegisterAliasGroup`). The flows below
assume `AliasColocated` (the default mode). `AliasSharded`'s equivalent flows are described
within Section 7's algorithm subsections — the visible behavior (which key resolves to what) is
identical between modes; only the underlying operations (one same-slot Lua vs. a record-slot Lua
plus a best-effort pointer write) differ.

**PutAliased("5", {email, foo@bar.com}, writer)**

1. Run `writer` → value; marshal once.
2. Compute the jittered TTL once.
3. `PutByAlias`: set `bc:{user}:5` = value; set `bc:grp:{user}:email:foo@bar.com` = `5`;
 `HSET bc:memb:{user}:5 email foo@bar.com`; one-per-field replace / cross-primary steal cleanup
 as needed — all under the one TTL.

**GetByAlias({email, foo@bar.com})** (alias read)

1. `GetByAlias(bc:grp:{user}:email:foo@bar.com)` → reads the pointer to learn primary `5`,
 rebuilds `bc:{user}:5`, reads it → value (or either link gone → `ErrStoreMiss` → treated as a miss).
2. On a miss: run the caller's loader, read `CacheUniqueKey()` off the result, then
 `PutByAlias` to write the value and register this alias — the same rebuild `PutAliased`
 performs, done automatically.

**Get("5")** (primary read)

1. `GET bc:{user}:5` → value directly. No pointer step.

**Put("5", writer)** / **PutValue("5", val)** (update through the primary)

1. Resolve to the value key (`bc:{user}:5`); write via `PutByAlias` with no pointer (primary-only
 write), which still refreshes every existing group key's TTL.
2. Every alias now resolves to the new value on its next read.

**Evict("5")** (delete through the primary)

1. One atomic Lua on `{user}`: `HGETALL bc:memb:{user}:5`, `DEL bc:{user}:5` (the invalidation),
 delete every pointer key rebuilt from that HASH, delete the members index. Nothing leaks.

**EvictByAlias({email, foo@bar.com})** (delete through an alias — symmetric result, explicit call)

1. One atomic Lua on `{user}`: resolve the pointer to primary `5`, then cascade exactly as
 `Evict("5")` does. Nothing leaks.

---

## Decisions log


| #   | Decision                                                                            | Rationale                                                                                                   |
| --- | ----------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| D1  | Pointer indirection, single value copy (not value duplication)                      | One value key → deleting it invalidates all aliases atomically, even on cluster.                            |
| D2  | Pointer stores the full value key, not a bare primary                               | Resolver reads the value key verbatim; no reconstruction assumptions.                                       |
| D3  | Two indexes: pointer (lookup) + members index (cleanup + validate-on-read)          | Redis cannot answer primary→aliases without a SCAN; the members index is the inverse index.                 |
| D4  | Lua / Redis Function for the grouped path                                           | Only Lua does read-branch-delete-many atomically without a WATCH retry loop.                                |
| D5  | `AliasCacheStore` optional interface is a **factory**, `AliasGroup(ns, mode) AliasOps`, comma-ok at `RegisterAliasGroup`; `AliasOps`'s method names (`GetValue`/`PutValue`/`EvictByPrimary`/`GetByAlias`/`PutByAlias`/`EvictByAlias`) mirror `CacheStore`'s own verb style | Mirrors the shipped `BatchCacheStore` precedent; keeps `Cache[T]` backend-agnostic and free of any key-building; a factory (not a fixed pre-built-key struct) is what lets one interface serve two slot-placement strategies (D16). |
| D6  | Hash-tag `{ns}` on **all** keys of an `AliasColocated` entity — value, pointer, members | Co-locates the whole group on one slot → the entire grouped op is one atomic Lua. Price: that entity's values are pinned to one cluster node (no horizontal spread) — this is what D16's `AliasSharded` mode exists to avoid for large/hot entities. Non-aliasing caches keep un-tagged, distributing value keys. |
| D7  | One jittered TTL per grouped write, applied to every key                            | Prevents desynchronized expiry within a group.                                                              |
| D8  | Opt-in by dedicated constructor `RegisterAliasGroup` (not a flag); one group per cache; group namespace auto-derived from the cache name; panics if store lacks `AliasCacheStore` OR `T` is not `UniqueKeyed` | Constructor choice is the opt-in; non-aliasing caches keep the untouched fast path; misconfig crashes at init, not at first alias read. |
| D9  | Bloom filter deferred; pure optimization, no correctness role                       | Lua path is correct alone and handles non-grouped keys gracefully.                                          |
| D10 | Diff-and-clean stale pointers inside the Lua write (`AliasColocated`; see D18 for `AliasSharded`'s different, lazy approach) | Satisfies the "no leaks / clean all metadata" requirement on reassignment.                                  |
| D11 | One alias per `PutAliased`/`PutAliasedValue` call; lazy; `PutAliasedValue` is the value-held twin of `PutAliased` (no writer re-run) | Writers don't need to know the full alias set; aliases appear as paths do; avoids a redundant load when the caller already holds the value. |
| D11a | One alias per field per primary — re-registering a field replaces its old pointer (not additive) | Matches single-valued identity fields (one email, one slug); gives `PutByAlias` a well-defined diff to clean on reassignment (Section 11). |
| D12 | Alias access is **explicit**, via `AliasRef`-typed methods (`GetByAlias`, `EvictByAlias`) — not by overloading `GetByKey`/`EvictByKey` to accept "any key, primary or alias" | A single string key can't safely distinguish primary from alias if either value contains the field separator; explicit methods have no such ambiguity. `PutByKey`/`PutValueByKey`/`EvictByKey` keep taking only the primary key (the value-derived `Put`/`PutValue`/`Evict` derive it from `CacheUniqueKey()`); there is no `UpdateAliased` because a primary `PutByKey`/`PutValueByKey` already updates every alias's view. |
| D12a | `GetByAlias` is read-through via `UniqueKeyed`: on a miss it runs the loader, reads `CacheUniqueKey()` off the result, and rebuilds the group | Requested explicitly so alias reads get the same "miss → load → cache" ergonomics as `GetByKey`, without the caller needing to already know the primary key. |
| D13 | Per-entity group namespace (`{ns}`) → keys globally unique                          | No cross-entity alias collision; the merge/steal problem does not arise.                                    |
| D14 | All key strings built only in the `internal/keyspace` package                       | Single source of truth for the keyspace, per repo Redis-key-centralization rule; shared by `smartcache`, `memstore`, and `redisstore` so no backend or mode can diverge on key format. |
| D15 | Single-process (mems4) fully supported via `memstore` mutex                         | No cross-instance concern; strictly the easy case.                                                          |
| D16 | `AliasMode` is a per-cache choice (`EntityOptions.AliasMode`, default `AliasColocated`); `AliasSharded` distributes value+members per record and pointers per alias (Section 7) | Trades full atomicity for the absence of a single-node hotspot on Cluster deployments with a large or hot entity. |
| D17 | Members index is a Redis HASH (`field → aliasValue`) in **both** modes, not a SET of pointer keys (supersedes D3's original shape, not its intent) | A SET can only answer "which pointer keys exist"; a HASH additionally answers "what value is field X set to now", which `AliasSharded`'s validate-on-read depends on. Keeping one shape for both modes avoids a schema fork. |
| D18 | `AliasSharded`'s `PutByAlias` does not proactively diff-and-delete a stale same-field reverse pointer (unlike `AliasColocated`, D10) | Verified against `shardedOps.PutByAlias` and `record_put.lua`: a proactive lookup+delete would need a second cross-slot round trip inside the write, defeating the point of sharding. Correctness is kept by validate-on-read (D3, Section 7); cleanup is lazy — compare-and-delete on the next evict, or TTL expiry. |
| D19 | `AliasSharded`'s reverse pointer stores the bare primary key, not the full value key (unlike `AliasColocated`'s pointer, D2) | The value key lives on a different slot (`{ns:pk}`) than the pointer (`{ns:field:value}`); the record side rebuilds the value/members keys from the primary key it already has, so the pointer only needs to carry that primary key. |


---

## Testing decisions

A good test asserts **external behavior** through the highest existing seam, not internal
mechanics. The highest seam here is `Cache[T]`'s public methods driven over a store fake —
the same seam the current `cache_test.go` uses. Each layer that can be mode-agnostic is run
**parametrized over both `AliasMode` values** through a shared `bothModes(t, fn)` helper
(`alias_test.go`, `memstore/alias_test.go`), rather than duplicating the test bodies; mode-specific
behavior gets its own dedicated tests where the two strategies genuinely diverge.

- **`Cache[T]` behavior** (`alias_test.go`, over `memstore`, both modes via `bothModes`):
`PutAliased` write → `GetByKey` (primary) and `GetByAlias` (each field) both return the value;
`EvictByAlias`/`EvictByKey` (primary) both invalidate the whole group; `PutByKey`/`PutValueByKey` on the primary
is seen through every alias; one-per-field replacement and cross-primary steal cleanup leak
nothing; `GetByAlias` read-through rebuilds the group via `UniqueKeyed` on a miss
(`TestAlias_GetByAlias_ReadThroughRebuild`); `PutAliasedValue`; `GetManyByKey` over primary keys; a
non-alias-group cache (`Register`) returns the "not an alias group" error
(`TestAlias_NotAliasGroup_Errors`) — byte-for-byte regression guard for existing non-aliasing
behavior; `RegisterAliasGroup` panics on a non-`AliasCacheStore` store and on a `T` that is not
`UniqueKeyed`.
- **`AliasOps` behavior** (`memstore/alias_test.go`, over `memstore`'s `memAliasOps`, both modes
via `bothModes`): put→resolve→get; alias miss; evict by primary; evict by alias; one-per-field
replace; cross-primary steal; TTL expiry — proving `memstore` gives identical visible results for
both modes (Section 13, D-level: `memAliasOps` is strictly the union of both strategies' safety
properties).
- **`redisstore` `colocatedOps`** (`redisstore/alias_test.go`, over a fake `RedisConn`): the
`PutByAlias`/`GetByAlias`/`EvictByPrimary`/`EvictByAlias` Lua calls issue the expected same-slot
`Eval` shape; a missing pointer returns `ErrStoreMiss`; `GetValue` hit/miss; a `[]byte`-typed Lua
result is handled the same as a `string`-typed one.
- **`redisstore` `shardedOps`** (same file): `GetByAlias` resolves the pointer then validates
against the record's members HASH (`TestSharded_GetByAlias_ResolveThenValidate`); a pointer miss
and a resolve error both surface correctly; `PutByAlias` writes the record then the pointer
(`TestSharded_PutByAlias_RecordThenPointer`); `EvictByPrimary` evicts the record then runs the
compare-and-delete pass per returned member (`TestSharded_EvictByPrimary_EvictThenCompareDelete`);
`EvictByAlias` resolves-then-evicts, and handles both a pointer miss and a resolve error
(`TestSharded_EvictByAlias_*`); `GetValue` hit/miss.
- **`internal/keyspace`** (`keyspace_test.go`): every builder (`NonAliasKey`, `ValueKey`,
`MembersKey`, `PointerKey`, the three Colocated-only prefix helpers) asserted for exact key
strings and hash-tag placement in both the Colocated and Sharded `sharded bool` states, plus a
consistency check between the prefix helpers and the full-key builders.
- **`RegisterAliasGroup`** (`manager_test.go`, `alias_test.go`): with a non-`AliasCacheStore`
store it panics; with a `T` that is not `UniqueKeyed` it panics; with a valid store and
`UniqueKeyed` `T`, and either `AliasMode`, it succeeds.
- **Prior art:** `cache_test.go`, `getmany_test.go`, `redisstore_test.go`, `jitter_test.go`.

Unit tests only — no live Redis (fake `RedisConn`), consistent with the module's existing
test strategy. Module coverage gate: `make unit-test-coverage` (≥85%, verified passing at 85.1%
with `redisstore` individually at 84.6% after adding the `GetValue`/`EvictByAlias`/`[]byte`-result
tests the coverage pass surfaced as gaps).

---

## Out of scope

- The bloom-filter routing optimization (Section 10) — deferred; see companion doc + roadmap.
- Cross-instance stampede protection and two-level caching — separate roadmap items.
- Any cloud `Store` adapter, RBAC/authz re-basing, or consumer wiring in `smritea-cloud`.
- Automatic mode selection or migration between `AliasColocated` and `AliasSharded` for an
  existing entity — the mode is fixed at `RegisterAliasGroup` (Section 9) and does not change
  without a data migration. `AliasSharded` (Section 7) is the mitigation for a large or hot
  entity; choosing it is a manual, per-cache decision.

---

## Open implementation notes (carry into the spec, not blocking)

1. **Per-entity slot concentration (resolved via `AliasSharded`).** `{ns}` co-locates one
 aliasing entity's *entire* keyspace — values, pointers, and members — on one cluster slot under
 `AliasColocated`. This was flagged as an open risk in the original draft of this document; it is
 now directly addressed by the `AliasSharded` strategy (Section 7), which a cache can opt into
 via `EntityOptions.AliasMode` (Section 9) when its entity is large or hot. `AliasColocated`
 remains the default and stays the right choice for small/medium entities or single-instance
 deployments.
2. **Lua dynamic keys.** Pointer names come from `HVALS`/`HGETALL` on the members index at run
 time, not pre-declared in `KEYS[]`. Under `AliasColocated` they all share the `{ns}` slot, so the
 same-slot rule holds; the implementer must verify the target Redis version's rule on
 same-slot-but-undeclared key access in scripts. Under `AliasSharded` this does not arise —
 reverse pointers are never touched from inside the record-slot Lua.

