-- +goose Up
-- +goose StatementBegin
-- Before 0119, a successful provider turn could omit its final turn.plan event.
-- AO settled the turn and the plan activity status but left both structured plan
-- payloads at their last intermediate step state. Repair only successful turns:
-- failure, interruption, recovery, and cancellation do not prove unfinished
-- plan steps actually completed.
UPDATE conversation_activities
SET status = 'completed',
    summary = 'Plan ' || json_array_length(detail_json, '$.steps') || '/' ||
              json_array_length(detail_json, '$.steps') || ' steps done',
    detail_json = json_set(
        detail_json,
        '$.steps',
        (SELECT json_group_array(json_set(value, '$.status', 'completed'))
         FROM json_each(conversation_activities.detail_json, '$.steps'))
    ),
    revision = revision + 1,
    updated_at = COALESCE(
        (SELECT completed_at FROM conversation_turns WHERE id = conversation_activities.turn_id),
        updated_at
    )
WHERE kind = 'plan'
  AND json_valid(detail_json)
  AND json_type(detail_json, '$.steps') = 'array'
  AND EXISTS (
      SELECT 1 FROM json_each(conversation_activities.detail_json, '$.steps')
      WHERE json_extract(value, '$.status') <> 'completed'
  )
  AND turn_id IN (SELECT id FROM conversation_turns WHERE state = 'completed');

UPDATE conversation_turns
SET plan_json = json_set(
        plan_json,
        '$.steps',
        (SELECT json_group_array(json_set(value, '$.status', 'completed'))
         FROM json_each(conversation_turns.plan_json, '$.steps'))
    )
WHERE state = 'completed'
  AND json_valid(plan_json)
  AND json_type(plan_json, '$.steps') = 'array'
  AND EXISTS (
      SELECT 1 FROM json_each(conversation_turns.plan_json, '$.steps')
      WHERE json_extract(value, '$.status') <> 'completed'
  );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The provider never reported which intermediate steps were truly last after a
-- successful turn, so the repaired terminal inference cannot be reversed.
SELECT 1;
-- +goose StatementEnd
