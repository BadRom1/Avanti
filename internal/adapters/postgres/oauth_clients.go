package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ory/fosite"
)

// OAuthClient est un client OAuth enregistré : un logiciel autorisé à demander
// des jetons, pas un compte.
//
// Il étend [fosite.DefaultClient] de deux champs que le protocole ignore mais
// dont l'interface a besoin : le nom déclaré, que la page de consentement
// affiche, et la date d'enregistrement.
//
// Le nom est le seul champ de cette structure qui vienne d'un inconnu :
// l'enregistrement dynamique est ouvert, n'importe qui peut donc y écrire ce
// qu'il veut. Il est affiché tel quel sur la page de consentement, où
// l'échappement de html/template est ce qui le rend inoffensif — et où sa
// longueur est bornée à l'enregistrement, pour qu'il ne puisse pas noyer le
// reste de la page.
type OAuthClient struct {
	fosite.DefaultClient

	// Name est le client_name de RFC 7591.
	Name string
	// CreatedAt date l'enregistrement.
	CreatedAt time.Time

	// secretHash porte l'empreinte du secret telle qu'elle sort de la base, où
	// elle est NULL pour un client public. Elle n'existe que le temps de la
	// lecture ; [OAuthClient.applySecret] la reverse ensuite dans le champ que
	// fosite consulte.
	secretHash *string
}

// GetName rend le nom déclaré par le client.
//
// La méthode n'appartient à aucune interface de fosite : c'est une extension
// facultative, que l'adapter web reconnaît par assertion de type. C'est ce qui
// permet à la page de consentement de nommer le client sans qu'aucune des deux
// familles d'adapters n'ait à importer l'autre (R4 de docs/ARCHITECTURE.md).
func (c *OAuthClient) GetName() string {
	return c.Name
}

// GetRegisteredAt rend la date à laquelle le client s'est enregistré.
//
// Même dispositif que [OAuthClient.GetName], et pour un besoin voisin : la page
// de consentement l'affiche à côté du nom. Le nom est déclaré par le client et
// ne prouve rien ; la date, elle, est constatée par le serveur — un client qui
// se présente sous le nom d'un agent connu mais s'est enregistré il y a trois
// minutes se voit à cette ligne-là.
func (c *OAuthClient) GetRegisteredAt() time.Time {
	return c.CreatedAt
}

// applySecret recopie l'empreinte lue en base dans le champ que fosite
// interroge. Un client public n'en a pas, et le champ reste nil.
func (c *OAuthClient) applySecret() {
	if c.secretHash == nil {
		c.Secret = nil
		return
	}
	c.Secret = []byte(*c.secretHash)
}

// clientColumns et clientColumnsPrefixed décrivent les mêmes colonnes dans le
// même ordre, celui qu'attend [scanClient]. La seconde sert aux requêtes qui
// joignent la table des jetons, où le préfixe lève l'ambiguïté sur id.
const (
	clientColumns         = `id, name, secret_hash, redirect_uris, grant_types, response_types, scopes, audience, public, created_at`
	clientColumnsPrefixed = `c.id, c.name, c.secret_hash, c.redirect_uris, c.grant_types, c.response_types, c.scopes, c.audience, c.public, c.created_at`
)

// GetClient relit un client enregistré.
//
// Un client inconnu rend [fosite.ErrNotFound] : c'est l'erreur que fosite
// traduit en invalid_client vers l'appelant.
func (s *OAuthStore) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	const query = `SELECT ` + clientColumns + ` FROM oauth_clients WHERE id = $1`

	client, err := scanClient(s.q(ctx).QueryRow(ctx, query, id))
	if err != nil {
		return nil, err
	}

	return client, nil
}

// CreateClient enregistre un client issu de l'enregistrement dynamique.
//
// Le nom arrive à part parce qu'il n'a pas sa place dans [fosite.DefaultClient],
// qui ne connaît que le protocole. Le prendre en paramètre plutôt que d'inventer
// une structure partagée est ce qui garde adapters/web et adapters/postgres
// mutuellement ignorants : le seul vocabulaire commun est celui de fosite.
func (s *OAuthStore) CreateClient(ctx context.Context, client *fosite.DefaultClient, name string, registeredAt time.Time) error {
	if client == nil {
		return errors.New("postgres : client OAuth manquant")
	}

	const query = `
		INSERT INTO oauth_clients (` + clientColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	var secret any
	if len(client.Secret) > 0 {
		secret = string(client.Secret)
	}

	_, err := s.q(ctx).Exec(ctx, query,
		client.ID, name, secret, client.RedirectURIs, client.GrantTypes,
		client.ResponseTypes, client.Scopes, client.Audience, client.Public, registeredAt.UTC())
	if err != nil {
		return fmt.Errorf("enregistrement du client OAuth %s : %w", client.ID, err)
	}

	return nil
}

// CountClients compte les clients enregistrés.
//
// C'est ce qui plafonne l'enregistrement dynamique : le point de terminaison
// étant ouvert par construction, sans plafond il suffirait d'une boucle pour
// remplir la table.
func (s *OAuthStore) CountClients(ctx context.Context) (int, error) {
	const query = `SELECT count(*) FROM oauth_clients`

	var total int
	if err := s.q(ctx).QueryRow(ctx, query).Scan(&total); err != nil {
		return 0, fmt.Errorf("comptage des clients OAuth : %w", err)
	}

	return total, nil
}

// ClientAssertionJWTValid et SetClientAssertionJWT servent l'authentification de
// client par assertion JWT (private_key_jwt, client_secret_jwt).
//
// Avanti ne la propose pas : ses clients sont publics, et le document de
// métadonnées n'annonce que la méthode « none ». fosite n'appelle donc ces
// méthodes que si un appelant a envoyé un client_assertion qu'aucune
// configuration n'accepte, c'est-à-dire jamais dans un usage légitime.
//
// Elles refusent, plutôt que de rendre nil. Le sens de l'erreur est ce qui
// compte : refuser rend la requête invalide, alors qu'accepter reviendrait à
// déclarer « ce JTI n'a jamais servi » — donc à autoriser le rejeu d'une
// assertion, si la méthode venait à être activée sans que ces deux fonctions
// soient réécrites.
func (s *OAuthStore) ClientAssertionJWTValid(_ context.Context, _ string) error {
	return errors.New("postgres : authentification de client par assertion JWT non prise en charge")
}

// SetClientAssertionJWT refuse pour la même raison que
// [OAuthStore.ClientAssertionJWTValid].
func (s *OAuthStore) SetClientAssertionJWT(_ context.Context, _ string, _ time.Time) error {
	return errors.New("postgres : authentification de client par assertion JWT non prise en charge")
}

// scanClient reconstruit un client depuis une ligne, dans l'ordre de
// [clientColumns].
func scanClient(row interface{ Scan(dest ...any) error }) (*OAuthClient, error) {
	var client OAuthClient

	err := row.Scan(&client.ID, &client.Name, &client.secretHash, &client.RedirectURIs,
		&client.GrantTypes, &client.ResponseTypes, &client.Scopes, &client.Audience,
		&client.Public, &client.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lecture d'un client OAuth : %w", err)
	}

	client.applySecret()

	return &client, nil
}
