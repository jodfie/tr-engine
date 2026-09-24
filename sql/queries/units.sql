-- name: GetUnitByComposite :one
SELECT u.system_id, COALESCE(s.name, '') AS system_name, s.sysid,
    u.unit_id, COALESCE(u.alpha_tag, '') AS alpha_tag, COALESCE(u.alpha_tag_source, '') AS alpha_tag_source,
    u.first_seen, u.last_seen,
    u.last_event_type, u.last_event_time, u.last_event_tgid,
    COALESCE(tg.alpha_tag, '') AS last_event_tg_tag,
    COALESCE(u.recorder_alpha_tag, '') AS recorder_alpha_tag, u.recorder_alpha_tag_seen,
    COALESCE(u.ota_alpha_tag, '') AS ota_alpha_tag, u.ota_alpha_tag_first_seen, u.ota_alpha_tag_last_seen
FROM units u
JOIN systems s ON s.system_id = u.system_id
LEFT JOIN talkgroups tg ON tg.system_id = u.system_id AND tg.tgid = u.last_event_tgid
WHERE u.system_id = $1 AND u.unit_id = $2;

-- name: FindUnitSystems :many
SELECT u.system_id, COALESCE(s.name, '') AS system_name, s.sysid
FROM units u
JOIN systems s ON s.system_id = u.system_id AND s.deleted_at IS NULL
WHERE u.unit_id = $1;

-- name: UpdateUnitFields :exec
UPDATE units SET
    alpha_tag        = CASE WHEN @alpha_tag::text <> '' THEN @alpha_tag ELSE alpha_tag END,
    alpha_tag_source = CASE WHEN @alpha_tag_source::text <> '' THEN @alpha_tag_source ELSE alpha_tag_source END
WHERE system_id = @system_id AND unit_id = @unit_id;

-- name: UpsertUnit :one
-- alpha_tag keeps its manual > csv > mqtt priority. recorder_alpha_tag and
-- ota_alpha_tag are recorded independently of it; an empty value is a no-op,
-- and an older event never replaces a value observed at a later time.
-- ota_alpha_tag_first_seen resets when the OTA alias changes.
INSERT INTO units (system_id, unit_id, alpha_tag, first_seen, last_seen, last_event_type, last_event_time, last_event_tgid,
    recorder_alpha_tag, recorder_alpha_tag_seen,
    ota_alpha_tag, ota_alpha_tag_first_seen, ota_alpha_tag_last_seen)
VALUES (@system_id, @unit_id, @alpha_tag, @event_time, @event_time, @event_type, @event_time, @tgid,
    NULLIF(@alpha_tag, ''), CASE WHEN @alpha_tag <> '' THEN @event_time::timestamptz END,
    NULLIF(@ota_alpha_tag::text, ''),
    CASE WHEN @ota_alpha_tag::text <> '' THEN @event_time::timestamptz END,
    CASE WHEN @ota_alpha_tag::text <> '' THEN @event_time::timestamptz END)
ON CONFLICT (system_id, unit_id) DO UPDATE SET
    alpha_tag       = CASE WHEN COALESCE(units.alpha_tag_source, '') IN ('manual', 'csv') THEN units.alpha_tag
                           ELSE COALESCE(NULLIF(@alpha_tag, ''), units.alpha_tag) END,
    first_seen      = LEAST(units.first_seen, @event_time),
    last_seen       = GREATEST(units.last_seen, @event_time),
    last_event_type = CASE WHEN @event_time >= units.last_event_time THEN @event_type ELSE units.last_event_type END,
    last_event_time = GREATEST(units.last_event_time, @event_time),
    last_event_tgid = CASE WHEN @event_time >= units.last_event_time AND @tgid > 0 THEN @tgid ELSE units.last_event_tgid END,
    recorder_alpha_tag = CASE
        WHEN @alpha_tag <> '' AND (units.recorder_alpha_tag_seen IS NULL OR @event_time >= units.recorder_alpha_tag_seen)
        THEN @alpha_tag ELSE units.recorder_alpha_tag END,
    recorder_alpha_tag_seen = CASE WHEN @alpha_tag <> ''
        THEN GREATEST(units.recorder_alpha_tag_seen, @event_time) ELSE units.recorder_alpha_tag_seen END,
    -- Same alias again: move last_seen later. first_seen never moves earlier,
    -- since a late event may predate a change to another alias and back.
    -- Different alias: replace it (and restart first_seen) unless this event
    -- predates the current one.
    ota_alpha_tag = CASE
        WHEN @ota_alpha_tag::text = '' THEN units.ota_alpha_tag
        WHEN units.ota_alpha_tag IS NULL OR units.ota_alpha_tag = @ota_alpha_tag::text
          OR units.ota_alpha_tag_last_seen IS NULL OR @event_time >= units.ota_alpha_tag_last_seen
        THEN @ota_alpha_tag::text ELSE units.ota_alpha_tag END,
    ota_alpha_tag_first_seen = CASE
        WHEN @ota_alpha_tag::text = '' THEN units.ota_alpha_tag_first_seen
        WHEN units.ota_alpha_tag = @ota_alpha_tag::text THEN COALESCE(units.ota_alpha_tag_first_seen, @event_time)
        WHEN units.ota_alpha_tag IS NULL OR units.ota_alpha_tag_last_seen IS NULL
          OR @event_time >= units.ota_alpha_tag_last_seen THEN @event_time
        ELSE units.ota_alpha_tag_first_seen END,
    ota_alpha_tag_last_seen = CASE
        WHEN @ota_alpha_tag::text = '' THEN units.ota_alpha_tag_last_seen
        WHEN units.ota_alpha_tag = @ota_alpha_tag::text THEN GREATEST(units.ota_alpha_tag_last_seen, @event_time)
        WHEN units.ota_alpha_tag IS NULL OR units.ota_alpha_tag_last_seen IS NULL
          OR @event_time >= units.ota_alpha_tag_last_seen THEN @event_time
        ELSE units.ota_alpha_tag_last_seen END
RETURNING COALESCE(alpha_tag, '') AS alpha_tag, COALESCE(ota_alpha_tag, '') AS ota_alpha_tag;
