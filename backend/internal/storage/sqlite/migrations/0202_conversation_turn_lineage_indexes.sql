-- +goose Up
-- Ancestor-turn visibility checks probe both tables by turn and sequence.
CREATE INDEX IF NOT EXISTS idx_conversation_messages_turn_sequence
    ON conversation_messages (turn_id, sequence);
CREATE INDEX IF NOT EXISTS idx_conversation_activities_turn_sequence
    ON conversation_activities (turn_id, sequence);

-- +goose Down
DROP INDEX IF EXISTS idx_conversation_activities_turn_sequence;
DROP INDEX IF EXISTS idx_conversation_messages_turn_sequence;
