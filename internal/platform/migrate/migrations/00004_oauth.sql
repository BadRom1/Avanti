-- Le serveur d'autorisation OAuth 2.1 embarqué : les clients enregistrés, et les
-- codes et jetons en circulation.
--
-- --- Pourquoi des colonnes en anglais alors que la table users est en français
--
-- Les colonnes de users portent le vocabulaire métier du projet (nom_affichage,
-- empreinte_mdp). Ici, le vocabulaire n'est pas celui du chantier : c'est celui
-- des RFC 6749, 7591, 7636 et 7009, où « client_id », « redirect_uris » et
-- « code_challenge » sont des noms propres. Les traduire créerait exactement la
-- double traduction que le langage ubiquitaire sert à éviter, mais dans l'autre
-- sens — entre le code et la spécification qu'il implémente. La table sessions
-- est déjà dans ce cas, pour la même raison.
--
-- --- Ce qui n'est PAS stocké ici : les jetons
--
-- Aucune colonne ne contient un code d'autorisation, un jeton d'accès ou un
-- jeton de rafraîchissement. Ce que la base voit est leur *signature* HMAC-SHA,
-- que fosite recalcule à partir du jeton présenté et de la clé
-- AVANTI_OAUTH_SECRET. Deux conséquences, toutes deux voulues :
--
--   * une copie de la base ne permet pas de se faire passer pour un agent. Elle
--     dit quels jetons existent, pas comment les rejouer ;
--   * la clé HMAC est le seul secret à protéger, et sa rotation invalide tout
--     d'un coup — ce qui est le comportement souhaitable en cas de doute.
--
-- --- Pourquoi une seule table pour quatre natures d'enregistrement
--
-- fosite demande quatre magasins : codes d'autorisation, jetons d'accès, jetons
-- de rafraîchissement, et paramètres PKCE. Leur contenu est le même objet — une
-- requête OAuth gelée — et les distinguer en quatre tables imposerait de tenir
-- quatre fois le même schéma. La colonne kind les sépare, la clé primaire
-- composite (kind, signature) garde chaque famille dans son espace de noms.

-- +goose Up
CREATE TABLE oauth_clients (
    id             TEXT        PRIMARY KEY,
    name           TEXT        NOT NULL,
    -- NULL pour un client public — c'est le cas de tout agent IA, qui tourne
    -- chez un tiers et ne peut donc rien garder de confidentiel. La colonne
    -- existe pour qu'un client confidentiel puisse être ajouté sans migration.
    secret_hash    TEXT,
    redirect_uris  TEXT[]      NOT NULL,
    grant_types    TEXT[]      NOT NULL,
    response_types TEXT[]      NOT NULL,
    scopes         TEXT[]      NOT NULL,
    audience       TEXT[]      NOT NULL DEFAULT '{}',
    public         BOOLEAN     NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,

    CONSTRAINT oauth_clients_name_non_vide
        CHECK (btrim(name) <> ''),
    CONSTRAINT oauth_clients_redirect_uris_non_vide
        CHECK (cardinality(redirect_uris) > 0),
    -- Un client public n'a pas d'empreinte de secret, un client confidentiel en
    -- a une : l'incohérence est refusée par la base plutôt que découverte à
    -- l'authentification.
    CONSTRAINT oauth_clients_secret_coherent
        CHECK ((public AND secret_hash IS NULL) OR (NOT public AND secret_hash IS NOT NULL))
);

COMMENT ON TABLE oauth_clients IS
    'Clients OAuth 2.1, enregistrés dynamiquement (RFC 7591). Un client est un logiciel autorisé à demander des jetons, pas un compte.';
COMMENT ON COLUMN oauth_clients.name IS
    'Nom déclaré par le client à son enregistrement. Il est affiché sur la page de consentement : il est donc à traiter comme une chaîne hostile jusqu''à échappement.';

CREATE TABLE oauth_tokens (
    -- authorization_code, access_token, refresh_token ou pkce.
    kind               TEXT        NOT NULL,
    -- Signature HMAC du jeton, jamais le jeton lui-même.
    signature          TEXT        NOT NULL,
    -- Identifiant de la requête OAuth d'origine. Il est *partagé* par tous les
    -- jetons issus d'une même autorisation, et c'est ce qui permet de révoquer
    -- une famille entière : réutiliser un code ou un jeton de rafraîchissement
    -- fait tomber tout ce qui porte le même request_id.
    request_id         TEXT        NOT NULL,
    client_id          TEXT        NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    -- Identifiant du compte au nom duquel le jeton agit. Vide serait une
    -- anomalie : un jeton sans sujet n'autoriserait personne.
    subject            TEXT        NOT NULL,
    requested_scopes   TEXT[]      NOT NULL,
    granted_scopes     TEXT[]      NOT NULL,
    requested_audience TEXT[]      NOT NULL DEFAULT '{}',
    granted_audience   TEXT[]      NOT NULL DEFAULT '{}',
    -- Les paramètres de la requête que fosite juge nécessaires au point de
    -- terminaison de jeton, et eux seuls : il les filtre avant de nous les
    -- confier. C'est là que vivent code_challenge et code_challenge_method pour
    -- les enregistrements de nature pkce.
    form               JSONB       NOT NULL,
    -- La session fosite sérialisée : sujet, dates d'expiration par type de
    -- jeton. Aucun secret n'y figure.
    session            JSONB       NOT NULL,
    requested_at       TIMESTAMPTZ NOT NULL,
    -- Repère de ménage, pas une autorité : l'expiration qui fait foi est celle
    -- que fosite lit dans la session. Cette colonne n'existe que pour que la
    -- purge périodique sache quoi supprimer sans désérialiser chaque ligne.
    expires_at         TIMESTAMPTZ NOT NULL,
    -- Faux pour un enregistrement invalidé : code déjà échangé, jeton révoqué,
    -- jeton de rafraîchissement remplacé par sa rotation. La ligne est
    -- conservée, et c'est le cœur de la détection de rejeu — supprimer la ligne
    -- rendrait un code réutilisé indiscernable d'un code inventé, donc
    -- silencieux.
    active             BOOLEAN     NOT NULL DEFAULT TRUE,
    -- Pour un jeton de rafraîchissement, la signature du jeton d'accès émis en
    -- même temps que lui. fosite la transmet ; elle est conservée telle quelle.
    access_signature   TEXT,

    PRIMARY KEY (kind, signature),

    CONSTRAINT oauth_tokens_kind_connu
        CHECK (kind IN ('authorization_code', 'access_token', 'refresh_token', 'pkce')),
    CONSTRAINT oauth_tokens_subject_non_vide
        CHECK (subject <> '')
);

-- Révoquer une famille se fait par request_id : c'est le chemin d'accès de
-- RevokeAccessToken et RevokeRefreshToken, appelés à chaque rejeu détecté.
CREATE INDEX oauth_tokens_request_id_idx ON oauth_tokens (kind, request_id);

-- La purge périodique balaie par date d'expiration.
CREATE INDEX oauth_tokens_expires_at_idx ON oauth_tokens (expires_at);

COMMENT ON TABLE oauth_tokens IS
    'Codes d''autorisation, jetons et paramètres PKCE en circulation. La colonne signature porte une empreinte HMAC : le jeton lui-même n''est jamais stocké.';

-- +goose Down
DROP TABLE oauth_tokens;
DROP TABLE oauth_clients;
