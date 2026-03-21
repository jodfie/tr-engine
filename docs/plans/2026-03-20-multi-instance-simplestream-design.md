# Multi-Instance Simplestream via Source IP Auto-Caching

**Date:** 2026-03-20
**Status:** Approved
**Issue:** #1

## Summary

Enable multiple trunk-recorder instances to send simplestream audio to a single tr-engine instance. Currently all UDP packets are resolved against a single fixed `STREAM_INSTANCE_ID`, making multi-instance setups impossible. The fix: auto-learn the mapping from sender IP to instance ID on first successful identity resolution, then cache it for all subsequent packets.

## Problem

Simplestream UDP packets carry `short_name` (the system name) but no `instance_id`. The source IP is available from `ReadFromUDP` but is currently discarded (`simplestream.go:92`). A single `STREAM_INSTANCE_ID` config value is applied to all packets regardless of sender.

When two TR instances send audio with overlapping `short_name` values (e.g., both have a system named "butco"), the router cannot distinguish them. MQTT does not have this problem because each message carries `instance_id` in its JSON payload.

## Design

### Change 1: Capture source IP in SimplestreamSource

`simplestream.go:92` — stop discarding the `*net.UDPAddr` from `conn.ReadFromUDP(buf)`. Extract the sender IP and attach it to `AudioChunk` via a new `SourceAddr string` field on the struct in `stream.go`.

### Change 2: Source IP → Instance ID cache in AudioRouter

`router.go` gets a new `sourceMap map[string]string` (sender IP → instanceID). Protected by a `sync.RWMutex` since reads are hot-path and writes are rare (once per unique sender IP).

The `IdentityLookup` interface must be extended with `LookupByShortNameAny` so the router can call it through the interface (the router holds `IdentityLookup`, not the concrete `*IdentityResolver`).

On each chunk in `processChunk()`:

1. **No ShortName** (sendTGID packets): skip auto-cache entirely, use `STREAM_INSTANCE_ID` fallback. Never write to `sourceMap` on empty ShortName — a failed sendTGID lookup must not overwrite a valid cached mapping from a prior sendJSON packet.
2. **Cache hit:** `sourceMap[chunk.SourceAddr]` exists → use that cached instanceID for `LookupByShortName`
3. **Cache miss with ShortName:** call `LookupByShortNameAny(shortName, claimedInstanceIDs)`. On success → cache `IP → instanceID`, use for this and all future packets from that IP
4. **Resolution failure:** fall back to `STREAM_INSTANCE_ID` (backward compatibility for single-instance setups)

The cache is permanent for the process lifetime. TR instance IPs don't change during a run. No expiry or eviction needed.

### Change 3: New identity resolver method

`identity.go` gets `LookupByShortNameAny(shortName string, exclude map[string]bool) (systemID, siteID int, instanceID string, ok bool)`. Scans all identity cache entries for a matching `SystemName`, skipping entries whose `instanceID` is in the `exclude` set. Returns the first non-excluded match along with its `instanceID`. This is the "learn" path — called only once per unique source IP, not on every packet.

The `exclude` parameter is built from `sourceMap` values — the set of instanceIDs already claimed by other IPs. This prevents two TR instances with the same `short_name` from both resolving to the same entry. The existing `LookupByShortName(instanceID="", shortName)` fallback scan is similar but does not return `instanceID` or support exclusion, so a new method is warranted.

### Change 4: Deprecate STREAM_INSTANCE_ID

Keep the env var and config field. Detect explicit setting via `os.Getenv("STREAM_INSTANCE_ID") != ""` (distinguishes user-set from defaulted, since `caarlos0/env` always populates the struct). When explicitly set, log a deprecation notice at startup suggesting the auto-cache makes it unnecessary. Continue using it as the fallback for:
- `sendTGID` packets (no `short_name` metadata)
- Resolution failures before the cache is populated
- Users who prefer explicit control

## What Doesn't Change

- **`sendTGID` packets** — carry only a bare TGID, no `short_name`. Cannot be disambiguated across instances. Continue using `STREAM_INSTANCE_ID` fallback. This is a protocol-level limitation of the sendTGID format.
- **Single-instance setups** — work identically. The auto-cache learns the one mapping transparently.
- **MQTT identity resolution** — completely untouched. MQTT messages carry `instance_id` natively.
- **Audio deduplication** — the router's per-talkgroup dedup logic is unchanged.
- **Opus encoding** — unchanged, operates downstream of identity resolution.

## Edge Cases

### Duplicate short_names across instances

Two TR instances both have `"butco"`. The first packet to arrive triggers `LookupByShortNameAny("butco", exclude={})`, which returns whichever DB entry it finds first (e.g., instanceID `"tr-1"`). That IP is cached as `IP-A → "tr-1"`.

When the second TR instance's first packet arrives, `LookupByShortNameAny("butco", exclude={"tr-1"})` skips the already-claimed entry and resolves to `"tr-2"`. That IP is cached as `IP-B → "tr-2"`.

This works because each TR instance has a stable IP, and the exclude set prevents two IPs from claiming the same instanceID.

### Stale duplicate system entries in DB

If the DB has orphaned system entries from old `instance_id` changes, `LookupByShortNameAny` might resolve to the wrong entry on first packet. This is a data cleanup problem — the admin should merge or delete stale systems. The auto-cache does not make this worse than the current `STREAM_INSTANCE_ID` behavior.

### TR instance IP changes mid-run

If a TR instance's IP changes (DHCP lease renewal, container restart), the old IP mapping becomes stale (no packets arrive on it — harmless) and the new IP triggers a fresh cache-miss → resolution → cache-write. No data loss, just one extra resolution cycle.

## Files to Modify

| File | Change |
|------|--------|
| `internal/audio/stream.go` | Add `SourceAddr string` to `AudioChunk` struct |
| `internal/audio/simplestream.go` | Capture `*net.UDPAddr` from `ReadFromUDP`, set `chunk.SourceAddr` |
| `internal/audio/router.go` | Extend `IdentityLookup` interface with `LookupByShortNameAny`. Add `sourceMap`, `sourceMu`. Update `processChunk` with cache-hit/miss/fallback logic |
| `internal/ingest/identity.go` | Add `LookupByShortNameAny()` method with exclude set |
| `internal/config/config.go` | Add deprecation note to `STREAM_INSTANCE_ID` comment |
| `internal/ingest/pipeline.go` | Log deprecation warning when `os.Getenv("STREAM_INSTANCE_ID") != ""` |
| `sample.env` | Update `STREAM_INSTANCE_ID` description with deprecation note |
| `internal/audio/router_test.go` | Test cache-hit, cache-miss, exclude logic, sendTGID fallback |
| `internal/audio/simplestream_test.go` | Test `SourceAddr` population |
