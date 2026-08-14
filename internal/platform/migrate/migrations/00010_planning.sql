-- L'ordonnancement du chantier : les étapes de travaux, leurs dépendances, et
-- les jalons contractuels.
--
-- --- Pourquoi il n'y a PAS de colonne statut
--
-- Le statut d'une étape (prévue, en cours, terminée) est DÉRIVÉ de ses dates
-- réelles par le domaine (planning.Etape.Statut) : debut_reel absent = prévue,
-- posé = en cours, fin_reelle posée = terminée. Une colonne statut pourrait
-- mentir sur les dates — un « terminee » sans fin réelle — et chaque écriture
-- devrait tenir deux vérités d'accord. Même raisonnement pour le Gantt et les
-- retards : tout est recalculé au rendu, rien n'en est stocké
-- (docs/ARCHITECTURE.md §4).
--
-- --- Pourquoi devis_id est un TEXT sans clé étrangère
--
-- La colonne rattache l'étape au devis retenu qui la finance, et c'est une
-- référence FAIBLE au sens de R2 de docs/ARCHITECTURE.md : la table devis
-- appartient à un autre domaine, poser un REFERENCES recréerait en SQL le
-- couplage que le code refuse. Vide ('' et non NULL, comme les pièces du
-- domaine finance), l'étape n'est financée par aucun lot engagé.
--
-- --- Où vivent les invariants de graphe et de prérequis
--
-- « Les dépendances ne forment pas de cycle » n'est pas exprimable en
-- contrainte CHECK — une contrainte ne voit qu'une ligne, un cycle est une
-- propriété du graphe entier. Comme « une étape ne démarre pas avant ses
-- prérequis terminés », il est tenu par l'adapter postgres : toute écriture
-- d'étape se fait dans une transaction qui prend le verrou consultatif global
-- du planning (pg_advisory_xact_lock, voir adapters/postgres/planning.go) puis
-- REJOUE la vérification (planning.CheckAcyclic, relecture des prérequis) sur
-- l'état verrouillé. Les FK intra-domaine ci-dessous, elles, sont la règle —
-- R2 ne vaut qu'ENTRE domaines (cf. migration 00006).

-- +goose Up
CREATE TABLE etapes (
    id           UUID        PRIMARY KEY,
    nom          TEXT        NOT NULL,
    description  TEXT        NOT NULL DEFAULT '',
    debut_prevu  TIMESTAMPTZ NOT NULL,
    fin_prevue   TIMESTAMPTZ NOT NULL,
    -- Dates réelles : NULL tant que rien n'a commencé ou fini. C'est d'elles
    -- que le statut se dérive — voir l'en-tête du fichier.
    debut_reel   TIMESTAMPTZ,
    fin_reelle   TIMESTAMPTZ,
    -- Référence faible vers le devis retenu (R2), vide pour une étape sans
    -- financement rattaché. Pas de clé étrangère : autre domaine.
    devis_id     TEXT        NOT NULL DEFAULT '',
    cree_par     UUID        NOT NULL,
    cree_le      TIMESTAMPTZ NOT NULL,
    modifie_le   TIMESTAMPTZ NOT NULL,

    CONSTRAINT etapes_nom_non_vide
        CHECK (btrim(nom) <> ''),
    -- Les bornes du domaine (planning.maxNameLength et consorts), répétées
    -- pour qu'une écriture directe en psql ne puisse pas les contourner.
    CONSTRAINT etapes_nom_longueur
        CHECK (char_length(nom) <= 120),
    CONSTRAINT etapes_description_longueur
        CHECK (char_length(description) <= 2000),
    CONSTRAINT etapes_devis_id_longueur
        CHECK (char_length(devis_id) <= 255),
    -- Les dates prévues sont ordonnées ; l'égalité est permise, un lot d'une
    -- journée commence et finit le même jour.
    CONSTRAINT etapes_plage_prevue_coherente
        CHECK (fin_prevue >= debut_prevu),
    -- Une fin réelle sans début réel raconterait une étape finie jamais
    -- commencée — le statut dérivé deviendrait illisible.
    CONSTRAINT etapes_fin_reelle_apres_debut
        CHECK (
            fin_reelle IS NULL
            OR (debut_reel IS NOT NULL AND fin_reelle >= debut_reel)
        ),
    CONSTRAINT etapes_horodatages_coherents
        CHECK (modifie_le >= cree_le)
);

-- L'ordre du Gantt et des listes : début prévu puis identifiant.
CREATE INDEX etapes_par_debut_prevu ON etapes (debut_prevu, id);

CREATE TABLE etape_dependances (
    -- Les dépendances disparaissent avec l'étape qui les porte ; le prérequis,
    -- lui, ne se supprime pas tant qu'une étape le référence (pas de CASCADE
    -- sur prerequis_id : perdre silencieusement une garde de démarrage serait
    -- pire qu'un refus de suppression).
    etape_id     UUID NOT NULL REFERENCES etapes (id) ON DELETE CASCADE,
    prerequis_id UUID NOT NULL REFERENCES etapes (id),

    PRIMARY KEY (etape_id, prerequis_id),

    -- L'auto-référence est le plus petit des cycles ; les autres ne sont pas
    -- exprimables en CHECK — voir l'en-tête du fichier : c'est le verrou
    -- consultatif de l'adapter qui garantit l'acyclicité.
    CONSTRAINT etape_dependances_sans_auto_reference
        CHECK (etape_id <> prerequis_id)
);

-- La lecture inverse : « qui dépend de moi ? » — celle du rejeu des prérequis
-- au démarrage et des vérifications de graphe.
CREATE INDEX etape_dependances_par_prerequis ON etape_dependances (prerequis_id);

CREATE TABLE jalons (
    id          UUID        PRIMARY KEY,
    nom         TEXT        NOT NULL,
    date_prevue TIMESTAMPTZ NOT NULL,
    -- NULL tant que le jalon n'est pas atteint — le dérivé « atteint » suit le
    -- même raisonnement que le statut des étapes.
    atteint_le  TIMESTAMPTZ,
    cree_par    UUID        NOT NULL,
    cree_le     TIMESTAMPTZ NOT NULL,
    modifie_le  TIMESTAMPTZ NOT NULL,

    CONSTRAINT jalons_nom_non_vide
        CHECK (btrim(nom) <> ''),
    CONSTRAINT jalons_nom_longueur
        CHECK (char_length(nom) <= 120),
    CONSTRAINT jalons_horodatages_coherents
        CHECK (modifie_le >= cree_le)
);

CREATE INDEX jalons_par_date ON jalons (date_prevue, id);

COMMENT ON TABLE etapes IS
    'Étapes de travaux du chantier. Le statut (prévue/en cours/terminée) est DÉRIVÉ des dates réelles par le domaine, jamais stocké ; le Gantt est recalculé au rendu.';
COMMENT ON COLUMN etapes.devis_id IS
    'Identifiant du devis retenu qui finance l''étape, vide sinon. Référence faible : aucune clé étrangère (R2 de docs/ARCHITECTURE.md).';
COMMENT ON COLUMN etapes.cree_par IS
    'Identifiant du compte qui a créé l''étape. Référence faible vers identity, sans clé étrangère (R2 de docs/ARCHITECTURE.md).';
COMMENT ON TABLE etape_dependances IS
    'Prérequis entre étapes. L''acyclicité du graphe n''est pas exprimable en contrainte : elle est rejouée par l''adapter postgres sous le verrou consultatif global du planning.';
COMMENT ON TABLE jalons IS
    'Jalons contractuels du chantier. « Atteint » est dérivé d''atteint_le, jamais stocké à part.';

-- +goose Down
DROP TABLE jalons;
DROP TABLE etape_dependances;
DROP TABLE etapes;
