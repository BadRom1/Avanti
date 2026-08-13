-- La consultation des artisans : les demandes envoyées, et les devis reçus en
-- réponse. Deux tables, parce qu'il y a deux durées de vie et deux invariants
-- distincts — une demande existe avant d'avoir reçu quoi que ce soit, et c'est
-- le devis qui porte un statut.
--
-- --- Pourquoi les montants sont des BIGINT de centimes
--
-- Ni NUMERIC, ni double precision. Le flottant est écarté sans discussion :
-- 11 800,50 n'a pas de représentation binaire exacte, et c'est précisément le
-- chiffre que l'utilisateur vérifie contre le papier de l'artisan. NUMERIC
-- serait exact, mais il fait entrer une décimale dans un domaine qui n'en veut
-- pas : le Go manipule des centimes entiers de bout en bout (devis.Montant), et
-- une colonne décimale obligerait à convertir aux deux extrémités, donc à
-- choisir un arrondi, donc à en discuter. Le centime entier ferme la question.
--
-- --- Pourquoi cree_par n'est pas une clé étrangère vers users
--
-- La colonne porte l'identifiant du compte qui a signé l'action, pour la
-- traçabilité. Ce n'est pas une jointure : R2 de docs/ARCHITECTURE.md veut que
-- les références inter-domaines soient des identifiants faibles, et le domaine
-- devis ne connaît pas identity — il ne saurait pas quoi faire d'un compte.
-- Poser la contrainte ici recréerait en base le couplage que le code refuse, et
-- ferait dépendre l'écriture d'un devis de la table des comptes.
--
-- Le prix est connu : un identifiant peut ne plus résoudre. Il est faible en
-- pratique, parce qu'un compte n'est jamais supprimé — seulement désactivé
-- (voir la migration 00002).
--
-- --- Pourquoi les artisans sollicités sont en JSONB
--
-- Ce sont des valeurs, pas des entités : elles n'ont pas d'identifiant, ne se
-- partagent pas entre demandes et n'existent pas hors de celle qui les porte.
-- Une table fille leur donnerait une identité qu'elles n'ont pas, et une
-- jointure à chaque lecture de la liste des demandes. Le jour où un carnet
-- d'adresses d'artisans deviendra utile, il naîtra comme tel, avec sa table et
-- ses propres invariants — et les devis déjà reçus continueront de porter la
-- copie du nom qui figurait sur le papier.

-- +goose Up
CREATE TABLE demandes_devis (
    id          UUID        PRIMARY KEY,
    -- Intitulé du lot de travaux consulté : « Charpente », « Électricité ».
    lot         TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    artisans    JSONB       NOT NULL DEFAULT '[]'::jsonb,
    envoyee_le  TIMESTAMPTZ NOT NULL,
    cree_par    UUID        NOT NULL,
    cree_le     TIMESTAMPTZ NOT NULL,
    modifie_le  TIMESTAMPTZ NOT NULL,

    CONSTRAINT demandes_devis_lot_non_vide
        CHECK (btrim(lot) <> ''),
    CONSTRAINT demandes_devis_lot_longueur
        CHECK (char_length(lot) <= 120),
    CONSTRAINT demandes_devis_artisans_tableau
        CHECK (jsonb_typeof(artisans) = 'array'),
    CONSTRAINT demandes_devis_horodatages_coherents
        CHECK (modifie_le >= cree_le)
);

CREATE INDEX demandes_devis_par_envoi ON demandes_devis (envoyee_le DESC, cree_le DESC);

CREATE TABLE devis (
    id            UUID        PRIMARY KEY,
    demande_id    UUID        NOT NULL REFERENCES demandes_devis (id) ON DELETE CASCADE,
    -- L'artisan est recopié dans le devis plutôt que référencé : ce qui figure
    -- sur le papier reçu ne doit pas changer quand la demande est corrigée.
    entreprise    TEXT        NOT NULL,
    email         TEXT        NOT NULL DEFAULT '',
    telephone     TEXT        NOT NULL DEFAULT '',
    montant       BIGINT      NOT NULL,
    recu_le       TIMESTAMPTZ NOT NULL,
    -- Durée de validité annoncée par l'artisan, à compter de recu_le. L'INTERVAL
    -- vaut zéro quand elle n'est pas renseignée, le cas le plus courant.
    validite      INTERVAL    NOT NULL DEFAULT INTERVAL '0',
    notes         TEXT        NOT NULL DEFAULT '',
    statut        TEXT        NOT NULL,
    -- Identifiants du domaine document, en référence faible (R2) : aucune clé
    -- étrangère, parce que la table des pièces appartient à un autre domaine et
    -- qu'un devis reste lisible quand une pièce a disparu.
    document_ids  TEXT[]      NOT NULL DEFAULT '{}',
    saisi_par     UUID        NOT NULL,
    decide_par    UUID,
    decide_le     TIMESTAMPTZ,
    cree_le       TIMESTAMPTZ NOT NULL,
    modifie_le    TIMESTAMPTZ NOT NULL,

    CONSTRAINT devis_entreprise_non_vide
        CHECK (btrim(entreprise) <> ''),
    -- Le montant est en centimes, strictement positif : un devis à zéro n'est
    -- pas un devis, et un montant négatif est une saisie retournée. La borne
    -- haute est celle du domaine (devis.MaxMontant), répétée ici pour qu'une
    -- écriture directe en psql ne puisse pas la contourner.
    CONSTRAINT devis_montant_positif
        CHECK (montant > 0 AND montant <= 10000000000),
    CONSTRAINT devis_validite_positive
        CHECK (validite >= INTERVAL '0'),
    -- Les statuts sont énumérés ici en plus de l'être dans le domaine. C'est le
    -- même doublon assumé que pour les rôles (migration 00002) : il fait
    -- qu'aucun statut inventé ne peut dormir en base en attendant qu'un code le
    -- lise.
    CONSTRAINT devis_statut_connu
        CHECK (statut IN ('recu', 'retenu', 'refuse')),
    -- Un devis tranché porte sa décision, un devis reçu n'en porte aucune. Sans
    -- cette contrainte, un « recu » daté d'une décision passerait inaperçu et
    -- l'interface afficherait une décision qui n'a pas eu lieu.
    CONSTRAINT devis_decision_coherente
        CHECK (
            (statut = 'recu'  AND decide_par IS NULL     AND decide_le IS NULL)
            OR
            (statut <> 'recu' AND decide_par IS NOT NULL AND decide_le IS NOT NULL)
        ),
    CONSTRAINT devis_horodatages_coherents
        CHECK (modifie_le >= cree_le)
);

CREATE INDEX devis_par_demande ON devis (demande_id, montant);

-- L'invariant central du domaine, tenu par la base et non par la seule
-- discipline du code : une demande ne porte qu'un seul devis retenu.
--
-- L'index est unique et partiel — il ne contraint que les lignes retenues, et
-- laisse donc autant de devis reçus ou refusés qu'on veut sur la même demande.
-- C'est ce qui fait que deux personnes qui tranchent la même comparaison au
-- même instant ne produisent pas deux devis retenus : la seconde transaction
-- est refusée par PostgreSQL, pas par une lecture qui aurait pu être périmée.
CREATE UNIQUE INDEX devis_un_seul_retenu_par_demande
    ON devis (demande_id) WHERE statut = 'retenu';

COMMENT ON TABLE demandes_devis IS
    'Consultations envoyées aux artisans. Une demande regroupe les devis concurrents d''un même lot de travaux.';
COMMENT ON COLUMN demandes_devis.cree_par IS
    'Identifiant du compte qui a ouvert la demande. Référence faible vers identity, sans clé étrangère (R2 de docs/ARCHITECTURE.md).';
COMMENT ON TABLE devis IS
    'Propositions chiffrées reçues des artisans. Le montant est en centimes d''euro, jamais en flottant.';
COMMENT ON COLUMN devis.montant IS
    'Montant TTC proposé, en centimes d''euro. Strictement positif.';
COMMENT ON COLUMN devis.document_ids IS
    'Pièces jointes, désignées par identifiant du domaine document. Référence faible : aucune clé étrangère, un devis reste lisible quand une pièce a disparu.';

-- +goose Down
DROP TABLE devis;
DROP TABLE demandes_devis;
