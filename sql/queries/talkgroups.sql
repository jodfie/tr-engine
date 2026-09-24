-- name: GetTalkgroupByComposite :one
SELECT t.system_id, COALESCE(s.name, '') AS system_name, s.sysid,
    t.tgid, COALESCE(t.alpha_tag, '') AS alpha_tag, COALESCE(t.tag, '') AS tag,
    COALESCE(t."group", '') AS "group", COALESCE(t.description, '') AS description,
    t.mode, t.priority, t.first_seen, t.last_seen,
    (SELECT count(*)::int FROM calls c WHERE c.system_id = t.system_id AND c.tgid = t.tgid AND c.start_time > now() - interval '30 days') AS call_count,
    (SELECT count(*)::int FROM calls c WHERE c.system_id = t.system_id AND c.tgid = t.tgid AND c.start_time > now() - interval '1 hour') AS calls_1h,
    (SELECT count(*)::int FROM calls c WHERE c.system_id = t.system_id AND c.tgid = t.tgid AND c.start_time > now() - interval '24 hours') AS calls_24h,
    GREATEST(
        (SELECT count(DISTINCT unit_rid)::int FROM unit_events ue WHERE ue.system_id = t.system_id AND ue.tgid = t.tgid AND ue.time > now() - interval '30 days'),
        (SELECT count(DISTINCT u)::int FROM calls c, unnest(c.unit_ids) AS u WHERE c.system_id = t.system_id AND c.tgid = t.tgid AND c.start_time > now() - interval '30 days' AND c.unit_ids IS NOT NULL)
    )::int AS unit_count
FROM talkgroups t
JOIN systems s ON s.system_id = t.system_id
WHERE t.system_id = $1 AND t.tgid = $2;

-- name: FindTalkgroupSystems :many
SELECT t.system_id, COALESCE(s.name, '') AS system_name, s.sysid
FROM talkgroups t
JOIN systems s ON s.system_id = t.system_id AND s.deleted_at IS NULL
WHERE t.tgid = $1;

-- name: UpdateTalkgroupFields :exec
UPDATE talkgroups SET
    alpha_tag        = CASE WHEN @alpha_tag::text <> '' THEN @alpha_tag ELSE alpha_tag END,
    alpha_tag_source = CASE WHEN @alpha_tag_source::text <> '' THEN @alpha_tag_source ELSE alpha_tag_source END,
    description = CASE WHEN @description::text <> '' THEN @description ELSE description END,
    "group"     = CASE WHEN @tg_group::text <> '' THEN @tg_group ELSE "group" END,
    tag         = CASE WHEN @tag::text <> '' THEN @tag ELSE tag END,
    priority    = CASE WHEN @priority::int >= 0 THEN @priority ELSE priority END
WHERE system_id = @system_id AND tgid = @tgid;

-- name: UpsertTalkgroup :one
INSERT INTO talkgroups (system_id, tgid, alpha_tag, tag, "group", description, first_seen, last_seen)
VALUES (@system_id, @tgid, @alpha_tag, @tag, @tg_group, @description, @event_time, @event_time)
ON CONFLICT (system_id, tgid) DO UPDATE SET
    alpha_tag   = CASE WHEN COALESCE(talkgroups.alpha_tag_source, '') IN ('manual', 'csv') THEN talkgroups.alpha_tag
                       ELSE COALESCE(NULLIF(@alpha_tag, ''), talkgroups.alpha_tag) END,
    tag         = CASE WHEN COALESCE(talkgroups.alpha_tag_source, '') IN ('manual', 'csv') THEN talkgroups.tag
                       ELSE COALESCE(NULLIF(@tag, ''), talkgroups.tag) END,
    "group"     = CASE WHEN COALESCE(talkgroups.alpha_tag_source, '') IN ('manual', 'csv') THEN talkgroups."group"
                       ELSE COALESCE(NULLIF(@tg_group, ''), talkgroups."group") END,
    description = CASE WHEN COALESCE(talkgroups.alpha_tag_source, '') IN ('manual', 'csv') THEN talkgroups.description
                       ELSE COALESCE(NULLIF(@description, ''), talkgroups.description) END,
    first_seen  = LEAST(talkgroups.first_seen, @event_time),
    last_seen   = GREATEST(talkgroups.last_seen, @event_time)
RETURNING COALESCE(alpha_tag, '') AS alpha_tag;

-- name: UpsertTalkgroupDirectory :exec
INSERT INTO talkgroup_directory (system_id, tgid, alpha_tag, mode, description, tag, category, priority)
VALUES (@system_id, @tgid, @alpha_tag, @mode, @description, @tag, @category, @priority)
ON CONFLICT (system_id, tgid) DO UPDATE SET
    alpha_tag   = COALESCE(NULLIF(@alpha_tag, ''), talkgroup_directory.alpha_tag),
    mode        = COALESCE(NULLIF(@mode, ''), talkgroup_directory.mode),
    description = COALESCE(NULLIF(@description, ''), talkgroup_directory.description),
    tag         = COALESCE(NULLIF(@tag, ''), talkgroup_directory.tag),
    category    = COALESCE(NULLIF(@category, ''), talkgroup_directory.category),
    priority    = COALESCE(@priority, talkgroup_directory.priority),
    imported_at = now();

-- name: EnrichTalkgroupsFromDirectory :execrows
-- Applies the talkgroup directory (the user's imported talkgroup CSV) to heard
-- talkgroups. Tag priority is manual > csv > mqtt:
--   * manual rows: user edits win; the directory only fills empty fields.
--   * any other row with a directory alpha_tag takes that tag (replacing an
--     MQTT-discovered one) and becomes alpha_tag_source = 'csv'. For csv rows
--     the directory is authoritative for tag/group/description/mode/priority
--     too: non-empty directory values replace current ones. UpsertTalkgroup
--     freezes these fields for csv rows, so leftover per-instance MQTT values
--     would otherwise stick forever and CSV edits would never propagate.
--   * rows without a directory alpha_tag keep fill-if-empty semantics.
-- Directory modes outside the talkgroups.mode CHECK set (e.g. RadioReference
-- "DE"/"TE") are ignored so one bad row cannot abort a bulk enrichment.
-- Runs on every call, so only rows whose values actually change are written
-- (the IS DISTINCT FROM guard repeats the SET expressions); the affected-row
-- count is the number of talkgroups changed.
UPDATE talkgroups t SET
    alpha_tag = CASE WHEN COALESCE(t.alpha_tag_source, '') = 'manual'
                     THEN COALESCE(NULLIF(t.alpha_tag, ''), d.alpha_tag, t.alpha_tag)
                     ELSE COALESCE(d.alpha_tag, t.alpha_tag) END,
    alpha_tag_source = CASE WHEN COALESCE(t.alpha_tag_source, '') = 'manual' THEN t.alpha_tag_source
                            WHEN d.alpha_tag IS NOT NULL THEN 'csv'
                            ELSE t.alpha_tag_source END,
    tag = CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
               THEN COALESCE(d.tag, t.tag)
               ELSE COALESCE(NULLIF(t.tag, ''), d.tag, t.tag) END,
    "group" = CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
                   THEN COALESCE(d.category, t."group")
                   ELSE COALESCE(NULLIF(t."group", ''), d.category, t."group") END,
    description = CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
                       THEN COALESCE(d.description, t.description)
                       ELSE COALESCE(NULLIF(t.description, ''), d.description, t.description) END,
    mode = CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
                THEN COALESCE(d.mode, t.mode)
                ELSE COALESCE(t.mode, d.mode) END,
    priority = CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
                    THEN COALESCE(d.priority, t.priority)
                    ELSE COALESCE(t.priority, d.priority) END
FROM (
    SELECT td.system_id, td.tgid,
        NULLIF(btrim(td.alpha_tag), '')   AS alpha_tag,
        NULLIF(btrim(td.tag), '')         AS tag,
        NULLIF(btrim(td.category), '')    AS category,
        NULLIF(btrim(td.description), '') AS description,
        CASE WHEN upper(btrim(td.mode)) IN ('D', 'A', 'E', 'M', 'T') THEN upper(btrim(td.mode)) END AS mode,
        td.priority
    FROM talkgroup_directory td
) d
WHERE d.system_id = t.system_id AND d.tgid = t.tgid
  AND t.system_id = @system_id
  AND (@tgid::int = 0 OR t.tgid = @tgid)
  AND (t.alpha_tag, t.alpha_tag_source, t.tag, t."group", t.description, t.mode, t.priority) IS DISTINCT FROM (
    CASE WHEN COALESCE(t.alpha_tag_source, '') = 'manual'
         THEN COALESCE(NULLIF(t.alpha_tag, ''), d.alpha_tag, t.alpha_tag)
         ELSE COALESCE(d.alpha_tag, t.alpha_tag) END,
    CASE WHEN COALESCE(t.alpha_tag_source, '') = 'manual' THEN t.alpha_tag_source
         WHEN d.alpha_tag IS NOT NULL THEN 'csv'
         ELSE t.alpha_tag_source END,
    CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
         THEN COALESCE(d.tag, t.tag)
         ELSE COALESCE(NULLIF(t.tag, ''), d.tag, t.tag) END,
    CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
         THEN COALESCE(d.category, t."group")
         ELSE COALESCE(NULLIF(t."group", ''), d.category, t."group") END,
    CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
         THEN COALESCE(d.description, t.description)
         ELSE COALESCE(NULLIF(t.description, ''), d.description, t.description) END,
    CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
         THEN COALESCE(d.mode, t.mode)
         ELSE COALESCE(t.mode, d.mode) END,
    CASE WHEN COALESCE(t.alpha_tag_source, '') <> 'manual' AND (d.alpha_tag IS NOT NULL OR t.alpha_tag_source = 'csv')
         THEN COALESCE(d.priority, t.priority)
         ELSE COALESCE(t.priority, d.priority) END
  );
