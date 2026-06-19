-- Decision Messages
-- Migration: 002_decision_messages
--
-- Stores conversation history for Decision review cycles, particularly
-- for roadmap_review decisions where humans and agents iterate on proposals.

CREATE TABLE decision_messages (
    id UUID PRIMARY KEY,
    decision_id UUID NOT NULL REFERENCES decisions(id) ON DELETE CASCADE,

    -- Who sent this message?
    --   'user'  - Human reviewer
    --   'agent' - AI agent (proposer, reviser, etc.)
    --   'system' - System-generated messages (status changes, etc.)
    role TEXT NOT NULL CHECK (role IN ('user', 'agent', 'system')),

    content TEXT NOT NULL,

    -- Token usage for agent messages (for budget tracking)
    tokens_used INTEGER,

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_decision_messages_decision ON decision_messages(decision_id, created_at);
