-- +goose Up
-- +goose StatementBegin
-- ReconcileConversationAuthentication runs after every completed chat turn while
-- holding the store's write lock. Its evidence check needs one turn.completed
-- event on the active branch; without this index SQLite walks every provider
-- event on the branch, which grows with the whole stream history.
CREATE INDEX IF NOT EXISTS idx_conversation_provider_events_turn_completed
    ON conversation_provider_events (conversation_id, branch_id)
    WHERE method = 'turn.completed';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_conversation_provider_events_turn_completed;
-- +goose StatementEnd
