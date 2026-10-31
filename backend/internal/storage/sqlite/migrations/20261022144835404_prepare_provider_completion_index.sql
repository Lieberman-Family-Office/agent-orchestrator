-- +goose Up
-- Release the fork's index name before upstream creates its replacement.
DROP INDEX IF EXISTS idx_conversation_provider_events_turn_completed;

-- +goose Down
CREATE INDEX IF NOT EXISTS idx_conversation_provider_events_turn_completed
    ON conversation_provider_events (conversation_id, branch_id)
    WHERE method = 'turn.completed';
