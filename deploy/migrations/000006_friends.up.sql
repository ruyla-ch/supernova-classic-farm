CREATE TABLE IF NOT EXISTS friend_codes (
    player_id BIGINT UNSIGNED NOT NULL,
    code CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at_ms BIGINT NOT NULL,
    expires_at_ms BIGINT NOT NULL,
    PRIMARY KEY (player_id),
    UNIQUE KEY uq_friend_codes_code (code),
    KEY idx_friend_codes_expiry (expires_at_ms),
    CONSTRAINT fk_friend_codes_account
        FOREIGN KEY (player_id) REFERENCES accounts (player_id),
    CONSTRAINT chk_friend_codes_expiry CHECK (expires_at_ms > created_at_ms)
) ENGINE = InnoDB;

CREATE TABLE IF NOT EXISTS friend_relations (
    player_low_id BIGINT UNSIGNED NOT NULL,
    player_high_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at_ms BIGINT NOT NULL,
    updated_at_ms BIGINT NOT NULL,
    PRIMARY KEY (player_low_id, player_high_id),
    KEY idx_friend_relations_high (player_high_id, status, created_at_ms),
    KEY idx_friend_relations_low (player_low_id, status, created_at_ms),
    CONSTRAINT fk_friend_relations_low_account
        FOREIGN KEY (player_low_id) REFERENCES accounts (player_id),
    CONSTRAINT fk_friend_relations_high_account
        FOREIGN KEY (player_high_id) REFERENCES accounts (player_id),
    CONSTRAINT chk_friend_relations_order CHECK (player_low_id < player_high_id),
    CONSTRAINT chk_friend_relations_status CHECK (status = 'ACTIVE')
) ENGINE = InnoDB;

INSERT IGNORE INTO schema_migrations (version) VALUES (6);
