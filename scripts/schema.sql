-- schema.sql
-- Creates the "items" table if it does not already exist.
--
-- This is identical to the Java version's schema.sql.
-- PostgreSQL runs this script on container startup via the
-- POSTGRES_INITDB_ARGS mechanism or through our docker-compose entrypoint.

CREATE TABLE IF NOT EXISTS items (
    id          BIGINT       PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    price       INTEGER      NOT NULL,
    description TEXT         NOT NULL
);
