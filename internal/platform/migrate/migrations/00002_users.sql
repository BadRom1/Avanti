-- Les comptes d'Avanti : deux propriétaires, et éventuellement un collaborateur
-- extérieur. La table est petite par nature — elle ne grandira pas au fil des
-- chantiers — mais elle porte la clé de tout le reste, d'où les contraintes.
--
-- --- Pourquoi email TEXT + index unique, et non citext
--
-- Deux façons courantes de rendre l'email insensible à la casse : le type citext,
-- ou un index unique sur lower(email). Aucune des deux n'est retenue telle
-- quelle : c'est le *domaine* qui normalise l'adresse (identity.NormaliserEmail
-- la met en minuscules et retire les espaces), et la base se contente d'exiger
-- que ce soit fait.
--
-- Le motif :
--
--   * citext demande CREATE EXTENSION, donc des droits de superutilisateur au
--     premier démarrage. Pour une application qu'un particulier auto-héberge —
--     parfois sur un PostgreSQL géré qui restreint les extensions — c'est une
--     dépendance d'installation à ne pas prendre pour ce seul besoin ;
--   * un index sur lower(email) laisserait la colonne contenir « Romain@… » tout
--     en interdisant le doublon. La valeur stockée cesse alors d'être la valeur
--     canonique, et deux couches peuvent ne plus être d'accord sur ce que « la
--     même adresse » veut dire ;
--   * la normalisation dans le domaine est une ligne de Go testable sans
--     PostgreSQL, et l'unicité redevient un index ordinaire, exact et portable.
--
-- La contrainte users_email_canonique est le garde-fou de ce choix : elle rejette
-- toute écriture qui n'aurait pas normalisé, y compris celle d'un psql à la main
-- ou d'un futur chemin de code qui court-circuiterait le domaine. Sans elle,
-- l'unicité ne vaudrait que la discipline de l'appelant.

-- +goose Up
CREATE TABLE users (
    id            UUID        PRIMARY KEY,
    email         TEXT        NOT NULL,
    nom_affichage TEXT        NOT NULL,
    empreinte_mdp TEXT        NOT NULL,
    role          TEXT        NOT NULL,
    actif         BOOLEAN     NOT NULL DEFAULT TRUE,
    cree_le       TIMESTAMPTZ NOT NULL,
    modifie_le    TIMESTAMPTZ NOT NULL,

    CONSTRAINT users_email_canonique
        CHECK (email = lower(email) AND email = btrim(email) AND email <> ''),
    CONSTRAINT users_email_longueur
        CHECK (char_length(email) <= 254),
    CONSTRAINT users_nom_affichage_non_vide
        CHECK (btrim(nom_affichage) <> ''),
    CONSTRAINT users_empreinte_non_vide
        CHECK (empreinte_mdp <> ''),
    -- Les rôles sont énumérés ici en plus de l'être dans le domaine. C'est un
    -- doublon assumé : il fait qu'aucun rôle inventé ne peut dormir en base en
    -- attendant qu'un code le lise. Le prix est connu — ajouter un rôle demande
    -- une migration — et il est bas pour une application à deux profils.
    CONSTRAINT users_role_connu
        CHECK (role IN ('proprietaire', 'collaborateur')),
    CONSTRAINT users_horodatages_coherents
        CHECK (modifie_le >= cree_le)
);

CREATE UNIQUE INDEX users_email_unique ON users (email);

COMMENT ON TABLE users IS
    'Comptes d''Avanti. L''email est l''identifiant de connexion, toujours normalisé par le domaine identity.';
COMMENT ON COLUMN users.empreinte_mdp IS
    'Empreinte argon2id complète, paramètres et sel inclus. Jamais le mot de passe.';
COMMENT ON COLUMN users.actif IS
    'Faux pour un compte désactivé. Un compte n''est jamais supprimé : les actions qu''il a signées dans les autres domaines continuent de le désigner.';

-- +goose Down
DROP TABLE users;
