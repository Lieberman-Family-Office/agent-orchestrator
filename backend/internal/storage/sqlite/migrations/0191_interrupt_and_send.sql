-- +goose Up
-- Bind a priority prompt to the exact provider turn it was accepted to interrupt.
ALTER TABLE conversation_turns ADD COLUMN interrupt_target_provider_turn_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_turns ADD COLUMN interrupt_delivered BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE conversation_turns DROP COLUMN interrupt_delivered;
ALTER TABLE conversation_turns DROP COLUMN interrupt_target_provider_turn_id;
