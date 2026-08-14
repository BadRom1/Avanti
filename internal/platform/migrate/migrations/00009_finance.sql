-- L'argent du chantier : les factures reçues et les acomptes versés, chacun
-- avec son suivi d'indemnisation par l'assurance. Deux tables, parce que ce
-- sont deux natures de pièces — une facture attend un règlement, un acompte
-- EST un règlement — même si elles partagent le suivi assurance.
--
-- --- Pourquoi les montants sont des BIGINT de centimes
--
-- Même décision que pour les devis (migration 00006) : ni NUMERIC ni flottant.
-- 11 800,50 € n'a pas de représentation binaire exacte, et c'est le chiffre
-- que l'assurance vérifiera contre les pièces. Le Go manipule des centimes
-- entiers de bout en bout (finance.Montant) ; le centime entier ferme la
-- question de l'arrondi.
--
-- --- Pourquoi devis_id est un TEXT sans clé étrangère
--
-- La colonne rattache la pièce au devis retenu qu'elle exécute, et c'est une
-- référence FAIBLE au sens de R2 de docs/ARCHITECTURE.md : la table devis
-- appartient à un autre domaine, poser un REFERENCES recréerait en SQL le
-- couplage que le code refuse. Le type est TEXT et non UUID pour la même
-- raison — c'est l'identifiant d'un autre domaine, la base n'a pas à en
-- connaître la forme. Vide ('' et non NULL, comme la cible des documents),
-- la pièce est une dépense hors devis : achat direct, auto-construction.
--
-- --- Pourquoi les contraintes CHECK doublent le domaine
--
-- Statuts, moyens de paiement, bornes et cohérences sont déjà vérifiés par
-- internal/finance. C'est le même doublon assumé que pour les statuts de devis
-- (migration 00006) : une écriture directe en psql ne peut pas déposer une
-- valeur inventée qui dormirait en base en attendant qu'un code la lise.
--
-- --- Où vit l'invariant du cumul des acomptes
--
-- « Le cumul des acomptes d'un devis ne dépasse pas le montant engagé » ne
-- peut PAS être une contrainte de cette table : le montant engagé vit dans la
-- table devis, qu'aucune contrainte d'ici n'a le droit de regarder (R2). Il
-- est tenu par l'adapter postgres, qui sérialise les insertions d'un même
-- devis_id par un verrou consultatif transactionnel (pg_advisory_xact_lock
-- sur hashtext(devis_id)) puis revérifie le cumul dans la transaction. L'index
-- partiel ci-dessous sert cette relecture.

-- +goose Up
CREATE TABLE factures (
    id                UUID        PRIMARY KEY,
    -- Référence faible vers le devis retenu (R2), vide pour une dépense hors
    -- devis.
    devis_id          TEXT        NOT NULL DEFAULT '',
    entreprise        TEXT        NOT NULL,
    montant           BIGINT      NOT NULL,
    -- Date que porte la pièce, distincte de cree_le : une facture reçue par
    -- courrier se saisit après coup.
    date_piece        TIMESTAMPTZ NOT NULL,
    numero            TEXT        NOT NULL DEFAULT '',
    notes             TEXT        NOT NULL DEFAULT '',
    statut_paiement   TEXT        NOT NULL,
    payee_le          TIMESTAMPTZ,
    statut_assurance  TEXT        NOT NULL,
    envoyee_le        TIMESTAMPTZ,
    montant_rembourse BIGINT      NOT NULL DEFAULT 0,
    rembourse_le      TIMESTAMPTZ,
    saisi_par         UUID        NOT NULL,
    cree_le           TIMESTAMPTZ NOT NULL,
    modifie_le        TIMESTAMPTZ NOT NULL,

    CONSTRAINT factures_devis_id_longueur
        CHECK (char_length(devis_id) <= 255),
    CONSTRAINT factures_entreprise_non_vide
        CHECK (btrim(entreprise) <> ''),
    -- La borne du domaine (finance.maxEntrepriseLength), répétée comme les
    -- autres.
    CONSTRAINT factures_entreprise_longueur
        CHECK (char_length(entreprise) <= 200),
    -- Le montant est en centimes, strictement positif et borné par le domaine
    -- (finance.MaxMontant), répété ici pour qu'une écriture directe en psql ne
    -- puisse pas le contourner.
    CONSTRAINT factures_montant_positif
        CHECK (montant > 0 AND montant <= 10000000000),
    CONSTRAINT factures_numero_longueur
        CHECK (char_length(numero) <= 80),
    CONSTRAINT factures_notes_longueur
        CHECK (char_length(notes) <= 2000),
    CONSTRAINT factures_statut_paiement_connu
        CHECK (statut_paiement IN ('impayee', 'payee')),
    -- Une facture payée porte sa date de paiement, une impayée n'en porte
    -- aucune. Sans cette contrainte, un « impayee » daté d'un règlement
    -- passerait inaperçu et l'interface afficherait un paiement qui n'a pas eu
    -- lieu.
    CONSTRAINT factures_paiement_coherent
        CHECK (
            (statut_paiement = 'impayee' AND payee_le IS NULL)
            OR
            (statut_paiement = 'payee'   AND payee_le IS NOT NULL)
        ),
    CONSTRAINT factures_statut_assurance_connu
        CHECK (statut_assurance IN ('non_envoyee', 'envoyee', 'remboursee')),
    -- Le suivi assurance avance d'un bloc : chaque état emporte exactement les
    -- horodatages et le montant que le domaine y pose. non_envoyee ⇒ rien ;
    -- envoyee ⇒ la date d'envoi, pas de remboursement ; remboursee ⇒ tout,
    -- avec 0 < montant_rembourse ≤ montant.
    CONSTRAINT factures_assurance_coherente
        CHECK (
            (statut_assurance = 'non_envoyee' AND envoyee_le IS NULL
                AND montant_rembourse = 0 AND rembourse_le IS NULL)
            OR
            (statut_assurance = 'envoyee' AND envoyee_le IS NOT NULL
                AND montant_rembourse = 0 AND rembourse_le IS NULL)
            OR
            (statut_assurance = 'remboursee' AND envoyee_le IS NOT NULL
                AND montant_rembourse > 0 AND montant_rembourse <= montant
                AND rembourse_le IS NOT NULL)
        ),
    CONSTRAINT factures_horodatages_coherents
        CHECK (modifie_le >= cree_le)
);

-- Le listing se lit de la pièce la plus récente à la plus ancienne ; la date de
-- saisie départage les pièces d'un même jour, l'identifiant fige l'ordre.
CREATE INDEX factures_par_date ON factures (date_piece DESC, cree_le DESC);

-- Les cumuls par devis de la synthèse. L'index est partiel : les dépenses hors
-- devis n'ont rien à y faire.
CREATE INDEX factures_par_devis ON factures (devis_id) WHERE devis_id <> '';

CREATE TABLE acomptes (
    id                UUID        PRIMARY KEY,
    devis_id          TEXT        NOT NULL DEFAULT '',
    entreprise        TEXT        NOT NULL,
    montant           BIGINT      NOT NULL,
    date_piece        TIMESTAMPTZ NOT NULL,
    moyen             TEXT        NOT NULL,
    notes             TEXT        NOT NULL DEFAULT '',
    statut_assurance  TEXT        NOT NULL,
    envoyee_le        TIMESTAMPTZ,
    montant_rembourse BIGINT      NOT NULL DEFAULT 0,
    rembourse_le      TIMESTAMPTZ,
    saisi_par         UUID        NOT NULL,
    cree_le           TIMESTAMPTZ NOT NULL,
    modifie_le        TIMESTAMPTZ NOT NULL,

    CONSTRAINT acomptes_devis_id_longueur
        CHECK (char_length(devis_id) <= 255),
    CONSTRAINT acomptes_entreprise_non_vide
        CHECK (btrim(entreprise) <> ''),
    CONSTRAINT acomptes_entreprise_longueur
        CHECK (char_length(entreprise) <= 200),
    CONSTRAINT acomptes_montant_positif
        CHECK (montant > 0 AND montant <= 10000000000),
    CONSTRAINT acomptes_notes_longueur
        CHECK (char_length(notes) <= 2000),
    -- Les moyens de paiement, répétés depuis le domaine
    -- (finance.AllMoyensPaiement) : une allow-list, pas une deny-list.
    CONSTRAINT acomptes_moyen_connu
        CHECK (moyen IN ('virement', 'cheque', 'especes', 'carte')),
    CONSTRAINT acomptes_statut_assurance_connu
        CHECK (statut_assurance IN ('non_envoyee', 'envoyee', 'remboursee')),
    CONSTRAINT acomptes_assurance_coherente
        CHECK (
            (statut_assurance = 'non_envoyee' AND envoyee_le IS NULL
                AND montant_rembourse = 0 AND rembourse_le IS NULL)
            OR
            (statut_assurance = 'envoyee' AND envoyee_le IS NOT NULL
                AND montant_rembourse = 0 AND rembourse_le IS NULL)
            OR
            (statut_assurance = 'remboursee' AND envoyee_le IS NOT NULL
                AND montant_rembourse > 0 AND montant_rembourse <= montant
                AND rembourse_le IS NOT NULL)
        ),
    CONSTRAINT acomptes_horodatages_coherents
        CHECK (modifie_le >= cree_le)
);

CREATE INDEX acomptes_par_date ON acomptes (date_piece DESC, cree_le DESC);

-- Le cumul des acomptes d'un devis — la relecture que l'adapter fait sous
-- verrou consultatif, et les cumuls de la synthèse.
CREATE INDEX acomptes_par_devis ON acomptes (devis_id) WHERE devis_id <> '';

COMMENT ON TABLE factures IS
    'Factures reçues des entreprises, ou dépenses directes. Montants en centimes d''euro, jamais en flottant.';
COMMENT ON COLUMN factures.devis_id IS
    'Identifiant du devis retenu que la facture exécute, vide pour une dépense hors devis. Référence faible : aucune clé étrangère (R2 de docs/ARCHITECTURE.md).';
COMMENT ON COLUMN factures.montant IS
    'Montant TTC de la facture, en centimes d''euro. Strictement positif.';
COMMENT ON COLUMN factures.montant_rembourse IS
    'Indemnité reçue de l''assurance, en centimes. Zéro tant que la pièce n''est pas remboursée, jamais supérieure au montant.';
COMMENT ON COLUMN factures.saisi_par IS
    'Identifiant du compte qui a saisi la pièce. Référence faible vers identity, sans clé étrangère (R2 de docs/ARCHITECTURE.md).';
COMMENT ON TABLE acomptes IS
    'Versements faits aux entreprises. L''invariant « cumul ≤ montant engagé du devis » est tenu par l''adapter postgres sous verrou consultatif, pas par une contrainte : le montant engagé vit dans un autre domaine (R2).';
COMMENT ON COLUMN acomptes.devis_id IS
    'Identifiant du devis retenu que l''acompte paie, vide pour un versement hors devis. Référence faible : aucune clé étrangère (R2 de docs/ARCHITECTURE.md).';
COMMENT ON COLUMN acomptes.moyen IS
    'Canal du versement : virement, cheque, especes ou carte.';

-- +goose Down
DROP TABLE acomptes;
DROP TABLE factures;
