package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// La mémoire des consentements : quel compte a déjà autorisé quel client.
//
// # Pourquoi une table plutôt qu'une requête sur les jetons
//
// La table oauth_tokens porte déjà le couple (subject, client_id) : y chercher
// une ligne dirait, en apparence, la même chose. En apparence seulement — ses
// lignes sont purgées à l'expiration (voir [OAuthStore.PurgeExpired]), et la
// réponse changerait donc toute seule avec le temps. Un client autorisé il y a
// six mois redeviendrait « jamais vu », c'est-à-dire que l'indicateur de la page
// de consentement mentirait précisément là où il sert : sur un client ancien,
// qu'un homonyme fraîchement enregistré pourrait imiter.
//
// Cette table-ci ne dit qu'une chose, et ne l'oublie pas : cette personne a déjà
// consenti à ce client, la première fois à telle date.

// HasGrant dit si ce compte a déjà autorisé ce client.
func (s *OAuthStore) HasGrant(ctx context.Context, userID, clientID string) (bool, error) {
	id, err := toUUID(identity.ID(userID))
	if err != nil {
		return false, err
	}

	const query = `SELECT EXISTS (SELECT 1 FROM oauth_grants WHERE user_id = $1 AND client_id = $2)`

	var granted bool
	if err := s.q(ctx).QueryRow(ctx, query, id, clientID).Scan(&granted); err != nil {
		return false, fmt.Errorf("lecture des autorisations OAuth du client %s : %w", clientID, err)
	}

	return granted, nil
}

// RecordGrant retient qu'un compte a autorisé un client.
//
// L'écriture est idempotente et ne réécrit pas la date : ce qui est conservé est
// la *première* fois, la seule information dont la page de consentement ait
// besoin. Un second consentement du même couple ne change donc rien, ce qui
// évite d'avoir à distinguer création et mise à jour côté appelant.
func (s *OAuthStore) RecordGrant(ctx context.Context, userID, clientID string, at time.Time) error {
	id, err := toUUID(identity.ID(userID))
	if err != nil {
		return err
	}

	const query = `
		INSERT INTO oauth_grants (user_id, client_id, first_granted_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, client_id) DO NOTHING`

	if _, err := s.q(ctx).Exec(ctx, query, id, clientID, at.UTC()); err != nil {
		return fmt.Errorf("enregistrement de l'autorisation OAuth du client %s : %w", clientID, err)
	}

	return nil
}
