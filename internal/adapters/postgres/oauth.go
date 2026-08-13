package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
)

// Natures d'enregistrement de la table oauth_tokens. Elles reprennent les quatre
// magasins que fosite demande ; la contrainte oauth_tokens_kind_connu les
// énumère aussi côté base, de sorte qu'une faute de frappe soit refusée à
// l'écriture plutôt que découverte à la lecture.
const (
	kindAuthorizationCode = "authorization_code"
	kindAccessToken       = "access_token"
	kindRefreshToken      = "refresh_token"
	kindPKCE              = "pkce"
)

// purgeHorizon borne la durée de conservation d'un enregistrement dont la
// session ne porte pas de date d'expiration.
//
// C'est un filet, pas une politique : fosite renseigne toujours l'expiration
// dans la session, pour les quatre natures d'enregistrement. La constante
// n'existe que pour qu'une ligne anormale finisse tout de même par disparaître,
// et elle est prise très large devant la durée de vie d'un jeton de
// rafraîchissement — purger un jeton encore valide serait bien pire que de
// garder trois mois une ligne morte.
const purgeHorizon = 90 * 24 * time.Hour

// OAuthStore implémente sur PostgreSQL les interfaces de stockage du serveur
// d'autorisation OAuth 2.1.
//
// Ce qu'il stocke n'est jamais un jeton : c'est la *signature* HMAC que fosite
// calcule à partir du jeton et de la clé de l'instance. Une lecture de la base
// dit donc quels jetons existent, sans donner le moyen de les rejouer.
//
// # Les séquences à plusieurs écritures sont transactionnelles
//
// fosite entoure ses séquences de plusieurs écritures — invalider un code puis
// créer les jetons qu'il donne, faire tourner un jeton de rafraîchissement puis
// émettre son remplaçant — d'un appel à son interface facultative
// storage.Transactional. Ce magasin l'implémente (voir oauth_tx.go), et ce n'est
// pas un confort : sans elle, les appels de la séquence sont indépendants, et
// deux requêtes concurrentes peuvent s'y intercaler. [OAuthStore.RotateRefreshToken]
// dit précisément ce que cela coûterait.
type OAuthStore struct {
	pool *pgxpool.Pool
}

// Les interfaces que fosite exige, vérifiées à la compilation.
//
// Sans ces déclarations, une signature manquante ne se verrait qu'au câblage :
// fosite compose son fournisseur par assertions de type non protégées, et une
// méthode absente y devient une panique au démarrage plutôt qu'une erreur de
// compilation.
var (
	_ fosite.ClientManager          = (*OAuthStore)(nil)
	_ oauth2.CoreStorage            = (*OAuthStore)(nil)
	_ oauth2.TokenRevocationStorage = (*OAuthStore)(nil)
	_ pkce.PKCERequestStorage       = (*OAuthStore)(nil)
)

// NewOAuthStore construit le magasin sur un pool existant. Le pool reste la
// propriété de l'appelant — cmd/avanti, qui l'ouvre et le ferme.
func NewOAuthStore(pool *pgxpool.Pool) (*OAuthStore, error) {
	if pool == nil {
		return nil, errors.New("postgres : pool de connexions manquant")
	}
	return &OAuthStore{pool: pool}, nil
}

// --- Codes d'autorisation ---------------------------------------------------

// CreateAuthorizeCodeSession gèle la requête autorisée, sous la signature du
// code remis au client.
func (s *OAuthStore) CreateAuthorizeCodeSession(ctx context.Context, code string, request fosite.Requester) error {
	return s.insert(ctx, kindAuthorizationCode, code, request, "")
}

// GetAuthorizeCodeSession relit la requête que le code désigne.
//
// Un code déjà consommé rend la requête *et* [fosite.ErrInvalidatedAuthorizeCode],
// jamais nil : c'est le contrat de fosite, qui se sert de la requête rendue pour
// révoquer tous les jetons issus de la même autorisation. Rendre nil ferait
// échouer la révocation en cascade sans que rien ne le signale.
func (s *OAuthStore) GetAuthorizeCodeSession(ctx context.Context, code string, session fosite.Session) (fosite.Requester, error) {
	request, active, err := s.fetch(ctx, kindAuthorizationCode, code, session)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInvalidatedAuthorizeCode
	}
	return request, nil
}

// InvalidateAuthorizeCodeSession marque le code comme consommé.
//
// La ligne est conservée. La supprimer rendrait un code rejoué indiscernable
// d'un code inventé — le premier doit faire tomber toute la famille de jetons,
// le second n'est qu'une requête invalide.
func (s *OAuthStore) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	return s.deactivateSignature(ctx, kindAuthorizationCode, code)
}

// --- Jetons d'accès ---------------------------------------------------------

// CreateAccessTokenSession enregistre un jeton d'accès émis.
func (s *OAuthStore) CreateAccessTokenSession(ctx context.Context, signature string, request fosite.Requester) error {
	return s.insert(ctx, kindAccessToken, signature, request, "")
}

// GetAccessTokenSession relit la requête d'un jeton d'accès. C'est le chemin de
// l'introspection, donc celui qu'emprunte toute vérification de jeton.
func (s *OAuthStore) GetAccessTokenSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.fetchActive(ctx, kindAccessToken, signature, session)
}

// DeleteAccessTokenSession efface un jeton d'accès.
func (s *OAuthStore) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	return s.delete(ctx, kindAccessToken, signature)
}

// --- Jetons de rafraîchissement ---------------------------------------------

// CreateRefreshTokenSession enregistre un jeton de rafraîchissement et la
// signature du jeton d'accès émis avec lui.
func (s *OAuthStore) CreateRefreshTokenSession(ctx context.Context, signature, accessSignature string, request fosite.Requester) error {
	return s.insert(ctx, kindRefreshToken, signature, request, accessSignature)
}

// GetRefreshTokenSession relit la requête d'un jeton de rafraîchissement.
//
// Comme pour les codes, un jeton désactivé rend la requête *et*
// [fosite.ErrInactiveToken] : c'est ce couple qui déclenche, côté fosite, la
// révocation de toute la famille quand un jeton déjà tourné est rejoué.
func (s *OAuthStore) GetRefreshTokenSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	request, active, err := s.fetch(ctx, kindRefreshToken, signature, session)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInactiveToken
	}
	return request, nil
}

// DeleteRefreshTokenSession efface un jeton de rafraîchissement.
func (s *OAuthStore) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	return s.delete(ctx, kindRefreshToken, signature)
}

// RotateRefreshToken retire de la circulation ce qu'un rafraîchissement
// remplace.
//
// La rotation est stricte : le jeton présenté et le jeton d'accès qu'il
// accompagnait cessent tous deux de valoir, à l'instant où leurs remplaçants
// sont émis. OAuth 2.1 l'exige des clients publics, et c'est ce qui donne son
// sens à la détection de rejeu — sans rotation, un jeton volé resterait utilisable
// aussi longtemps que l'original.
//
// La rotation se fait en deux temps : le jeton *présenté* d'abord, sous
// condition, puis le reste de la famille sans condition. Le premier temps décide
// qui gagne la course, le second garantit qu'aucune branche n'est oubliée.
//
// # C'est ici que se joue la concurrence, et le raisonnement mérite d'être écrit
//
// fosite appelle cette méthode entre BeginTX et CreateRefreshTokenSession : la
// désactivation de l'ancien jeton et la création du nouveau tiennent dans une
// même transaction (voir oauth_tx.go). Le point de sérialisation est
// [OAuthStore.rotatePresentedToken], dont la mise à jour porte sur *une* ligne —
// celle de la signature présentée — avec la condition « active = TRUE » et un
// décompte des lignes touchées.
//
// Deux requêtes présentant le *même* jeton de rafraîchissement valide se
// déroulent alors ainsi, en READ COMMITTED — le niveau par défaut de
// PostgreSQL, aucun réglage n'est nécessaire :
//
//  1. les deux lisent le jeton hors transaction et le trouvent actif : la
//     course existe bel et bien, et aucun verrou ne l'a empêchée ;
//  2. la première fait passer la ligne à active = FALSE, sans valider encore ;
//  3. la seconde exécute la même mise à jour, trouve la ligne verrouillée et
//     *attend* ;
//  4. la première valide. La seconde est réveillée et réévalue son prédicat sur
//     la version validée — où active vaut FALSE. Elle ne touche aucune ligne ;
//  5. zéro ligne touchée devient [fosite.ErrSerializationFailure], toute la
//     transaction de la seconde est annulée : ni jeton d'accès, ni jeton de
//     rafraîchissement n'en sortent.
//
// L'étape 4 est celle qui rend SERIALIZABLE inutile : en READ COMMITTED,
// PostgreSQL réévalue la clause WHERE d'un UPDATE bloqué après le déblocage,
// plutôt que d'agir sur la version qu'il avait lue. La condition « active =
// TRUE » est donc vérifiée sur l'état d'après le commit du concurrent. Sans
// elle, la seconde mise à jour réécrirait FALSE sur FALSE, compterait une ligne,
// et la course serait gagnée deux fois.
//
// # Pourquoi la signature, et non l'identifiant de requête
//
// Viser la famille entière — « désactive tout ce qui porte ce request_id et qui
// est encore actif » — semble équivalent et ne l'est pas. Les jetons successifs
// d'une même autorisation partagent leur identifiant de requête : après la
// rotation gagnante, la famille contient de nouveau une ligne active, celle qui
// vient d'être émise. Une concurrente arrivée un instant plus tard trouverait
// cette ligne-là, la désactiverait, compterait sa ligne — et gagnerait une course
// qu'elle a perdue, en révoquant au passage la paire de sa rivale. La signature
// présentée, elle, désigne une ligne et une seule : celle sur laquelle les deux
// requêtes doivent se disputer.
func (s *OAuthStore) RotateRefreshToken(ctx context.Context, requestID, signature string) error {
	if err := s.rotatePresentedToken(ctx, signature); err != nil {
		return err
	}
	// Le reste de la famille suit, sans condition : la course est déjà tranchée,
	// et il ne reste qu'à ne pas laisser en circulation un frère du jeton qu'on
	// vient de retirer.
	if err := s.RevokeRefreshToken(ctx, requestID); err != nil {
		return err
	}

	return s.RevokeAccessToken(ctx, requestID)
}

// rotatePresentedToken désactive le jeton de rafraîchissement présenté, à
// condition qu'il soit encore actif, et exige d'avoir touché sa ligne.
//
// C'est la différence avec [OAuthStore.deactivateSignature], qui ne distingue
// pas « déjà désactivé » de « désactivé maintenant » : une invalidation tolère de
// retrouver son travail déjà fait, une rotation non. Le jeton vient d'être lu
// actif par le gestionnaire de fosite ; s'il ne l'est plus à l'instant de
// l'écriture, c'est qu'une autre requête l'a fait tourner entre les deux, et
// émettre une seconde paire de jetons reviendrait à contourner la détection de
// rejeu que la rotation existe pour armer.
//
// [fosite.ErrSerializationFailure] est l'erreur que fosite attend dans ce cas
// précis : son gestionnaire de rafraîchissement la traduit en invalid_request
// assortie de « multiple concurrent requests using the same token. Please retry
// the request. ». L'appelant reçoit donc une réponse OAuth exacte et une
// consigne exécutable, là où une erreur quelconque deviendrait un server_error
// qui ne dirait ni ce qui s'est passé, ni quoi faire.
func (s *OAuthStore) rotatePresentedToken(ctx context.Context, signature string) error {
	const query = `
		UPDATE oauth_tokens SET active = FALSE
		 WHERE kind = $1 AND signature = $2 AND active = TRUE`

	tag, err := s.q(ctx).Exec(ctx, query, kindRefreshToken, signature)
	if err != nil {
		return fmt.Errorf("rotation du jeton de rafraîchissement OAuth : %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fosite.ErrSerializationFailure
	}

	return nil
}

// --- Révocation -------------------------------------------------------------

// RevokeRefreshToken désactive tous les jetons de rafraîchissement d'une
// autorisation.
func (s *OAuthStore) RevokeRefreshToken(ctx context.Context, requestID string) error {
	return s.deactivateFamily(ctx, kindRefreshToken, requestID)
}

// RevokeAccessToken désactive tous les jetons d'accès d'une autorisation.
func (s *OAuthStore) RevokeAccessToken(ctx context.Context, requestID string) error {
	return s.deactivateFamily(ctx, kindAccessToken, requestID)
}

// --- PKCE -------------------------------------------------------------------

// CreatePKCERequestSession retient le défi PKCE attaché à un code
// d'autorisation.
func (s *OAuthStore) CreatePKCERequestSession(ctx context.Context, signature string, requester fosite.Requester) error {
	return s.insert(ctx, kindPKCE, signature, requester, "")
}

// GetPKCERequestSession relit le défi PKCE d'un code.
func (s *OAuthStore) GetPKCERequestSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.fetchActive(ctx, kindPKCE, signature, session)
}

// DeletePKCERequestSession efface le défi, une fois le code échangé. Il ne sert
// qu'une fois par construction : le garder n'apporterait rien.
func (s *OAuthStore) DeletePKCERequestSession(ctx context.Context, signature string) error {
	return s.delete(ctx, kindPKCE, signature)
}

// --- Ménage -----------------------------------------------------------------

// PurgeExpired supprime les enregistrements périmés et rend leur nombre.
//
// Sans elle, la table ne ferait que grandir : chaque rafraîchissement y laisse
// deux lignes mortes, et une table de sécurité qui enfle indéfiniment finit par
// être une charge d'exploitation. La date lue est [purgeHorizon] après
// l'expiration réelle, pour qu'une horloge légèrement désaccordée entre deux
// démarrages ne fasse jamais disparaître un jeton encore valide.
func (s *OAuthStore) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	const query = `DELETE FROM oauth_tokens WHERE expires_at < $1`

	tag, err := s.q(ctx).Exec(ctx, query, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("purge des jetons OAuth expirés : %w", err)
	}

	return tag.RowsAffected(), nil
}

// --- Écriture et lecture communes -------------------------------------------

// tokenColumns est la liste d'insertion, alignée sur l'ordre des paramètres.
const tokenColumns = `kind, signature, request_id, client_id, subject,
	requested_scopes, granted_scopes, requested_audience, granted_audience,
	form, session, requested_at, expires_at, access_signature`

// insert gèle une requête OAuth sous une signature.
//
// La signature est la clé, et c'est ce qui rend la table sûre : deux jetons ne
// peuvent pas partager la leur, et une insertion en double est un conflit de clé
// primaire plutôt qu'un écrasement silencieux.
func (s *OAuthStore) insert(ctx context.Context, kind, signature string, request fosite.Requester, accessSignature string) error {
	if request == nil {
		return fmt.Errorf("postgres : requête OAuth manquante pour un enregistrement %s", kind)
	}

	form, err := json.Marshal(request.GetRequestForm())
	if err != nil {
		return fmt.Errorf("sérialisation des paramètres de la requête OAuth : %w", err)
	}
	session, err := json.Marshal(request.GetSession())
	if err != nil {
		return fmt.Errorf("sérialisation de la session OAuth : %w", err)
	}

	const query = `
		INSERT INTO oauth_tokens (` + tokenColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

	var access any
	if accessSignature != "" {
		access = accessSignature
	}

	_, err = s.q(ctx).Exec(ctx, query,
		kind, signature, request.GetID(), request.GetClient().GetID(), subjectOf(request),
		textArray(request.GetRequestedScopes()), textArray(request.GetGrantedScopes()),
		textArray(request.GetRequestedAudience()), textArray(request.GetGrantedAudience()),
		form, session, request.GetRequestedAt().UTC(), expiryOf(kind, request), access)
	if err != nil {
		return fmt.Errorf("enregistrement OAuth %s : %w", kind, err)
	}

	return nil
}

// fetchActive relit un enregistrement dont l'inactivité vaut absence.
//
// C'est le cas des jetons d'accès et des sessions PKCE : rien n'a besoin de
// distinguer « révoqué » de « inconnu », et ne pas le distinguer évite d'offrir
// un oracle à qui essaie des jetons.
func (s *OAuthStore) fetchActive(ctx context.Context, kind, signature string, session fosite.Session) (fosite.Requester, error) {
	request, active, err := s.fetch(ctx, kind, signature, session)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInactiveToken
	}
	return request, nil
}

// selectTokenQuery relit un enregistrement et le client qui l'a obtenu, en une
// seule interrogation.
const selectTokenQuery = `
	SELECT t.request_id, t.subject, t.requested_scopes, t.granted_scopes,
	       t.requested_audience, t.granted_audience, t.form, t.session,
	       t.requested_at, t.active,
	       ` + clientColumnsPrefixed + `
	  FROM oauth_tokens t
	  JOIN oauth_clients c ON c.id = t.client_id
	 WHERE t.kind = $1 AND t.signature = $2`

// fetch reconstruit la requête OAuth gelée sous une signature, et dit si elle
// est encore active.
//
// session est le réceptacle que fosite fournit : la session stockée y est
// désérialisée, plutôt que dans un type choisi ici. C'est ce qui permet à
// l'appelant de récupérer sa propre implémentation de session, avec les champs
// qu'il y a mis. La révocation, elle, appelle sans réceptacle ; on en fabrique
// un par défaut.
func (s *OAuthStore) fetch(ctx context.Context, kind, signature string, session fosite.Session) (*fosite.Request, bool, error) {
	if session == nil {
		session = new(fosite.DefaultSession)
	}

	var (
		request        fosite.Request
		subject        string
		rawForm        []byte
		rawSession     []byte
		active         bool
		requestedScope []string
		grantedScope   []string
		requestedAud   []string
		grantedAud     []string
		client         OAuthClient
	)

	row := s.q(ctx).QueryRow(ctx, selectTokenQuery, kind, signature)
	err := row.Scan(&request.ID, &subject, &requestedScope, &grantedScope,
		&requestedAud, &grantedAud, &rawForm, &rawSession, &request.RequestedAt, &active,
		&client.ID, &client.Name, &client.secretHash, &client.RedirectURIs, &client.GrantTypes,
		&client.ResponseTypes, &client.Scopes, &client.Audience, &client.Public, &client.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fosite.ErrNotFound
	}
	if err != nil {
		return nil, false, fmt.Errorf("lecture de l'enregistrement OAuth %s : %w", kind, err)
	}

	if err := json.Unmarshal(rawSession, session); err != nil {
		return nil, false, fmt.Errorf("lecture de la session OAuth %s : %w", kind, err)
	}
	form := url.Values{}
	if err := json.Unmarshal(rawForm, &form); err != nil {
		return nil, false, fmt.Errorf("lecture des paramètres de la requête OAuth %s : %w", kind, err)
	}

	client.applySecret()

	request.Client = &client
	request.Session = session
	request.Form = form
	request.RequestedScope = requestedScope
	request.GrantedScope = grantedScope
	request.RequestedAudience = requestedAud
	request.GrantedAudience = grantedAud

	return &request, active, nil
}

// deactivateSignature retire un enregistrement précis de la circulation sans
// l'effacer.
func (s *OAuthStore) deactivateSignature(ctx context.Context, kind, signature string) error {
	const query = `UPDATE oauth_tokens SET active = FALSE WHERE kind = $1 AND signature = $2`

	tag, err := s.q(ctx).Exec(ctx, query, kind, signature)
	if err != nil {
		return fmt.Errorf("invalidation de l'enregistrement OAuth %s : %w", kind, err)
	}
	if tag.RowsAffected() == 0 {
		return fosite.ErrNotFound
	}

	return nil
}

// deactivateFamily retire de la circulation tous les enregistrements d'une même
// autorisation.
//
// Zéro ligne touchée n'est pas une erreur : fosite révoque par précaution des
// familles qui n'ont parfois jamais eu de jeton de ce type, et transformer cela
// en échec ferait rater la révocation des autres.
func (s *OAuthStore) deactivateFamily(ctx context.Context, kind, requestID string) error {
	const query = `UPDATE oauth_tokens SET active = FALSE WHERE kind = $1 AND request_id = $2`

	if _, err := s.q(ctx).Exec(ctx, query, kind, requestID); err != nil {
		return fmt.Errorf("révocation des enregistrements OAuth %s : %w", kind, err)
	}

	return nil
}

// delete efface un enregistrement. Zéro ligne touchée vaut succès : l'appelant
// voulait que la ligne n'existe plus, et elle n'existe pas.
func (s *OAuthStore) delete(ctx context.Context, kind, signature string) error {
	const query = `DELETE FROM oauth_tokens WHERE kind = $1 AND signature = $2`

	if _, err := s.q(ctx).Exec(ctx, query, kind, signature); err != nil {
		return fmt.Errorf("suppression de l'enregistrement OAuth %s : %w", kind, err)
	}

	return nil
}

// textArray prépare une liste de chaînes pour une colonne TEXT[] NOT NULL.
//
// La conversion n'est pas cosmétique : fosite laisse ses listes à nil quand la
// requête ne les mentionne pas — une demande d'autorisation sans paramètre
// « audience » a une audience nil, pas vide — et pgx traduit une tranche nil en
// NULL. La colonne étant NOT NULL, l'insertion échouerait, et l'appelant
// recevrait un server_error dont rien n'expliquerait la cause.
func textArray(values fosite.Arguments) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// subjectOf rend l'identifiant du compte au nom duquel la requête agit.
func subjectOf(request fosite.Requester) string {
	session := request.GetSession()
	if session == nil {
		return ""
	}
	return session.GetSubject()
}

// expiryOf rend la date après laquelle la ligne ne sert plus à rien.
//
// Elle sort de la session, où fosite inscrit l'expiration de chaque type de
// jeton. Le repli sur [purgeHorizon] ne devrait jamais servir ; il est là pour
// qu'une ligne sans date ne devienne pas éternelle.
func expiryOf(kind string, request fosite.Requester) time.Time {
	session := request.GetSession()
	if session != nil {
		var tokenType fosite.TokenType
		switch kind {
		case kindAccessToken:
			tokenType = fosite.AccessToken
		case kindRefreshToken:
			tokenType = fosite.RefreshToken
		default:
			tokenType = fosite.AuthorizeCode
		}
		if expiry := session.GetExpiresAt(tokenType); !expiry.IsZero() {
			return expiry.UTC()
		}
	}

	return request.GetRequestedAt().UTC().Add(purgeHorizon)
}
