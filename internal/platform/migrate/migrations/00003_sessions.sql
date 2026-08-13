-- Le magasin de sessions de scs (github.com/alexedwards/scs), adossé à
-- PostgreSQL par pgxstore.
--
-- Le schéma n'est pas négociable : ces trois colonnes et leurs types sont ceux
-- que pgxstore interroge. Ne pas les renommer.
--
-- Pourquoi les sessions vivent en base et non en mémoire : un redémarrage du
-- binaire — mise à jour, redémarrage de l'hôte — ne doit pas déconnecter tout le
-- monde, et la déconnexion doit être réelle, c'est-à-dire effaçable côté serveur.
-- Un cookie signé sans état côté serveur ne permet ni l'une ni l'autre.
--
-- Le nettoyage des sessions expirées est fait par la goroutine que pgxstore
-- lance au démarrage, pas par un travail SQL périodique : c'est ce que la
-- bibliothèque fournit, et cela évite d'installer quoi que ce soit côté hôte.

-- +goose Up
CREATE TABLE sessions (
    token  TEXT        PRIMARY KEY,
    data   BYTEA       NOT NULL,
    expiry TIMESTAMPTZ NOT NULL
);

CREATE INDEX sessions_expiry_idx ON sessions (expiry);

COMMENT ON TABLE sessions IS
    'Sessions web, gérées par github.com/alexedwards/scs/pgxstore. Le schéma est imposé par la bibliothèque.';

-- +goose Down
DROP TABLE sessions;
