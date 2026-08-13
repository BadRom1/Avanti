-- Première migration d'Avanti : elle n'installe aucune table métier — celles-ci
-- arriveront avec le lot de leur domaine — mais elle donne au schéma une trace
-- de lui-même, utile dès le premier diagnostic d'une instance auto-hébergée.
--
-- goose tient déjà l'historique des versions dans goose_db_version. app_info
-- répond à une autre question : « quelle instance est-ce, et depuis quand ? »,
-- lisible d'un simple SELECT sans connaître l'outil de migration.

-- +goose Up
CREATE TABLE app_info (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE app_info IS 'Métadonnées de l''instance Avanti : couple clé/valeur, une ligne par fait.';

INSERT INTO app_info (key, value) VALUES
    ('schema_baseline', '00001_app_info'),
    ('instance_created_at', now()::text);

-- +goose Down
DROP TABLE app_info;
