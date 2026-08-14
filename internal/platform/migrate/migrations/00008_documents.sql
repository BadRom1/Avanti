-- Les pièces du dossier : devis signés, factures scannées, photos de chantier,
-- rapports d'expertise, courriers d'assurance. Une seule table, parce que la
-- base ne porte que les métadonnées — le contenu binaire vit dans le stockage
-- (disque local ou objet S3, au choix de la configuration) sous la clé de
-- l'identifiant, et ne passe jamais par PostgreSQL.
--
-- --- Pourquoi les contraintes CHECK doublent le domaine
--
-- Les types de contenu, les catégories et les bornes de taille sont déjà
-- vérifiés par internal/document. C'est le même doublon assumé que pour les
-- statuts de devis (migration 00006) : il fait qu'une écriture directe en psql
-- ne peut pas déposer une valeur inventée qui dormirait en base en attendant
-- qu'un code la lise.
--
-- --- Pourquoi televerse_par n'est pas une clé étrangère vers users
--
-- La colonne porte l'identifiant du compte qui a déposé la pièce, pour la
-- traçabilité. Ce n'est pas une jointure : R2 de docs/ARCHITECTURE.md veut que
-- les références inter-domaines soient des identifiants faibles, et le domaine
-- document ne connaît pas identity — il ne saurait pas quoi faire d'un compte.
-- Poser la contrainte ici recréerait en base le couplage que le code refuse.
-- Même raisonnement pour la cible : cible_id désigne un devis, une facture ou
-- une étape d'un autre domaine, sans REFERENCES — une pièce reste lisible
-- quand sa cible a disparu, c'est le prix connu de la référence faible.

-- +goose Up
CREATE TABLE documents (
    id            UUID        PRIMARY KEY,
    -- Nom de fichier d'origine, nettoyé par le domaine : sans chemin, sans
    -- caractère de contrôle. C'est le nom que le téléchargement rend.
    nom_fichier   TEXT        NOT NULL,
    mime          TEXT        NOT NULL,
    -- Taille du contenu en octets, telle que constatée au dépôt.
    taille        BIGINT      NOT NULL,
    categorie     TEXT        NOT NULL,
    description   TEXT        NOT NULL DEFAULT '',
    -- Rattachement par référence faible (R2) : un couple (type, identifiant),
    -- les deux vides pour une pièce libre. cible_id est du TEXT et non un
    -- UUID : c'est l'identifiant d'un autre domaine, la base n'a pas à en
    -- connaître la forme.
    cible_type    TEXT        NOT NULL DEFAULT '',
    cible_id      TEXT        NOT NULL DEFAULT '',
    televerse_par UUID        NOT NULL,
    cree_le       TIMESTAMPTZ NOT NULL,
    modifie_le    TIMESTAMPTZ NOT NULL,

    CONSTRAINT documents_nom_fichier_non_vide
        CHECK (btrim(nom_fichier) <> ''),
    CONSTRAINT documents_nom_fichier_longueur
        CHECK (char_length(nom_fichier) <= 255),
    -- La liste des types acceptés, répétée depuis le domaine
    -- (document.AllowedMimeTypes) : une allow-list, pas une deny-list.
    CONSTRAINT documents_mime_connu
        CHECK (mime IN ('application/pdf', 'image/jpeg', 'image/png', 'image/webp')),
    -- La borne haute est celle du domaine (document.MaxFileSize, 25 Mio).
    CONSTRAINT documents_taille_bornee
        CHECK (taille > 0 AND taille <= 26214400),
    CONSTRAINT documents_categorie_connue
        CHECK (categorie IN ('devis_signe', 'facture', 'photo_chantier',
                             'rapport_expertise', 'courrier_assurance', 'autre')),
    -- Le rattachement va par paire : les deux champs vides (pièce libre), ou
    -- les deux remplis avec un type connu. Un type sans identifiant serait un
    -- rattachement qui ne désigne rien.
    CONSTRAINT documents_cible_coherente
        CHECK (
            (cible_type = ''  AND cible_id = '')
            OR
            (cible_type IN ('devis', 'facture', 'etape') AND cible_id <> '')
        ),
    -- La borne du domaine (document.maxTargetIDLength), répétée ici — même
    -- doublon assumé que les autres CHECK : un identifiant réel est un UUID de
    -- 36 caractères, la colonne n'a pas à accepter un roman écrit en psql.
    CONSTRAINT documents_cible_id_longueur
        CHECK (char_length(cible_id) <= 255),
    CONSTRAINT documents_horodatages_coherents
        CHECK (modifie_le >= cree_le)
);

-- Le listing des pièces se lit de la plus récente à la plus ancienne.
CREATE INDEX documents_par_depot ON documents (cree_le DESC);

-- Les pièces d'une cible — celles d'un devis sur sa page de comparaison.
-- L'index est partiel : les pièces libres, majoritaires à terme (photos de
-- chantier), n'ont rien à y faire.
CREATE INDEX documents_par_cible ON documents (cible_type, cible_id)
    WHERE cible_type <> '';

COMMENT ON TABLE documents IS
    'Métadonnées des pièces du dossier. Le contenu binaire vit dans le stockage (disque ou S3) sous la clé de l''identifiant, jamais en base.';
COMMENT ON COLUMN documents.nom_fichier IS
    'Nom de fichier d''origine, nettoyé (sans chemin ni caractère de contrôle). C''est le nom rendu au téléchargement.';
COMMENT ON COLUMN documents.mime IS
    'Type de contenu constaté au dépôt par examen du contenu, pas celui annoncé par le client.';
COMMENT ON COLUMN documents.cible_type IS
    'Type de la cible justifiée (devis, facture, etape), vide pour une pièce libre. Référence faible : aucune clé étrangère (R2 de docs/ARCHITECTURE.md).';
COMMENT ON COLUMN documents.televerse_par IS
    'Identifiant du compte qui a déposé la pièce. Référence faible vers identity, sans clé étrangère (R2 de docs/ARCHITECTURE.md).';

-- +goose Down
DROP TABLE documents;
