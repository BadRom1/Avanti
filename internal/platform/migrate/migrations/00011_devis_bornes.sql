-- Alignement des tables du domaine devis (migration 00006) sur la convention
-- des lots suivants, et suppression d'un mécanisme mort.
--
-- --- Pourquoi une NOUVELLE migration
--
-- Les migrations publiées ne se modifient plus (docs/ARCHITECTURE.md §8) : les
-- fichiers sont embarqués dans le binaire et rejoués partout, et réécrire
-- 00006 rendrait deux instances divergentes sans que rien ne le signale. Les
-- corrections s'ajoutent donc ici.
--
-- --- Les bornes de longueur
--
-- Les tables des lots suivants (00008, 00009, 00010) doublent chaque borne du
-- domaine d'une contrainte CHECK, pour qu'une écriture directe en psql ne
-- puisse pas stocker ce que le code refuse. Les tables du devis, écrites en
-- premier, ne bornaient que le lot. Les valeurs ci-dessous sont celles du
-- domaine (internal/devis : artisan.go et demande.go), répétées à l'identique.
--
-- --- La colonne document_ids
--
-- Elle devait porter les pièces jointes d'un devis, par référence faible. Le
-- mécanisme réellement construit est l'inverse : c'est la pièce (domaine
-- document) qui porte sa cible — (type, identifiant), migration 00008 — et
-- AUCUN code n'a jamais écrit dans document_ids. Une colonne que rien ne
-- remplit est une promesse fausse pour qui lit le schéma : elle disparaît avec
-- le champ Go qui l'exposait.

-- +goose Up
ALTER TABLE demandes_devis
    ADD CONSTRAINT demandes_devis_description_longueur
        CHECK (char_length(description) <= 4000);

ALTER TABLE devis
    ADD CONSTRAINT devis_entreprise_longueur
        CHECK (char_length(entreprise) <= 200),
    ADD CONSTRAINT devis_email_longueur
        CHECK (char_length(email) <= 254),
    ADD CONSTRAINT devis_telephone_longueur
        CHECK (char_length(telephone) <= 40),
    ADD CONSTRAINT devis_notes_longueur
        CHECK (char_length(notes) <= 4000);

ALTER TABLE devis
    DROP COLUMN document_ids;

-- +goose Down
ALTER TABLE devis
    ADD COLUMN document_ids TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE devis
    DROP CONSTRAINT devis_entreprise_longueur,
    DROP CONSTRAINT devis_email_longueur,
    DROP CONSTRAINT devis_telephone_longueur,
    DROP CONSTRAINT devis_notes_longueur;

ALTER TABLE demandes_devis
    DROP CONSTRAINT demandes_devis_description_longueur;
