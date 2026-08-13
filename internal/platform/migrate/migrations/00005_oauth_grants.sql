-- La mémoire des consentements : quel compte a déjà autorisé quel client OAuth.
--
-- --- À quoi elle sert, et pourquoi elle ne peut pas être déduite
--
-- L'enregistrement dynamique des clients est ouvert par construction : c'est ce
-- que le modèle MCP demande, n'importe quel agent peut s'enregistrer sans
-- compte. Le corollaire est qu'un client_name ne prouve rien — rien n'empêche un
-- inconnu de s'enregistrer sous le nom d'un agent que l'utilisateur connaît, ni
-- de le faire une minute avant de demander une autorisation.
--
-- La page de consentement affiche donc, à côté du nom déclaré, deux faits que le
-- serveur constate lui-même : l'identifiant attribué au client, la date de son
-- enregistrement, et un troisième que cette table permet — « première
-- autorisation de ce client » ou « vous avez déjà autorisé ce client ». C'est le
-- seul de ces trois signaux qui distingue l'agent que la personne emploie depuis
-- des mois d'un homonyme apparu ce matin.
--
-- La table oauth_tokens porte déjà le couple (subject, client_id) et pourrait
-- sembler suffire. Elle ne suffit pas : ses lignes sont purgées à l'expiration,
-- et l'indicateur retomberait tout seul sur « première autorisation » pour un
-- client autorisé il y a longtemps — c'est-à-dire qu'il mentirait exactement là
-- où il compte. Ici, rien n'est purgé.
--
-- --- Ce qu'elle ne fait pas
--
-- Elle n'autorise rien et n'accorde rien : ni scope, ni durée, ni dispense de
-- consentement. Un client déjà connu repasse par la page de consentement comme
-- les autres, et le refus reste un refus. Elle n'a qu'un lecteur, l'indicateur
-- de la page.

-- +goose Up
CREATE TABLE oauth_grants (
    user_id          UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    client_id        TEXT        NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    -- Date du premier consentement. Les suivants ne la réécrivent pas :
    -- l'ancienneté de la relation est ce qui a une valeur de signal.
    first_granted_at TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (user_id, client_id)
);

COMMENT ON TABLE oauth_grants IS
    'Consentements accordés : un couple (compte, client) autorisé au moins une fois. Sert l''indicateur « première autorisation » de la page de consentement, jamais une décision d''autorisation.';

-- +goose Down
DROP TABLE oauth_grants;
