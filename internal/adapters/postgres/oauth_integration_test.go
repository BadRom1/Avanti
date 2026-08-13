package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Ces tests vérifient le **contrat** que fosite attend d'un magasin, pas le flux
// OAuth — celui-ci est exercé de bout en bout dans adapters/web et cmd/avanti.
//
// Ce qui se joue ici est le détail que rien d'autre ne rattraperait : un code
// invalidé doit rendre la requête *et* une erreur, une révocation doit porter sur
// toute une famille, un jeton effacé doit disparaître. fosite s'appuie sur ces
// nuances pour révoquer en cascade après un rejeu ; un magasin qui rendrait nil
// au lieu de la requête casserait la cascade sans faire échouer le cas heureux —
// c'est-à-dire silencieusement.

// newOAuthStore monte une base neuve et rend le magasin OAuth prêt à l'emploi.
func newOAuthStore(t *testing.T) *postgres.OAuthStore {
	t.Helper()

	pool := openPool(t, freshDatabase(t))
	applyMigrations(t, pool)

	store, err := postgres.NewOAuthStore(pool)
	if err != nil {
		t.Fatalf("postgres.NewOAuthStore() échoué : %v", err)
	}

	return store
}

// oauthStoreWithAccount monte le magasin OAuth et un compte réel dans la même
// base.
//
// La table des consentements référence users(id) : sans compte, aucune
// autorisation ne peut y être notée. C'est voulu — un consentement est celui
// d'une personne, et une ligne qui n'en désignerait aucune ne voudrait rien
// dire.
func oauthStoreWithAccount(t *testing.T) (*postgres.OAuthStore, identity.ID) {
	t.Helper()

	pool := openPool(t, freshDatabase(t))
	applyMigrations(t, pool)

	store, err := postgres.NewOAuthStore(pool)
	if err != nil {
		t.Fatalf("postgres.NewOAuthStore() échoué : %v", err)
	}

	repo, err := postgres.NewUserRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewUserRepo() échoué : %v", err)
	}

	user := testAccount(t, "consentement@exemple.fr", identity.RoleProprietaire)
	if err := repo.Create(t.Context(), user); err != nil {
		t.Fatalf("création du compte de test : %v", err)
	}

	return store, user.ID
}

// testClient enregistre un client public et le rend.
func testClient(t *testing.T, store *postgres.OAuthStore, id string) *fosite.DefaultClient {
	t.Helper()

	client := &fosite.DefaultClient{
		ID:            id,
		RedirectURIs:  []string{"https://agent.exemple.fr/callback"},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"},
		Scopes:        []string{"mcp", "devis:read"},
		Audience:      []string{},
		Public:        true,
	}

	if err := store.CreateClient(t.Context(), client, "Agent de test", time.Now()); err != nil {
		t.Fatalf("CreateClient() échoué : %v", err)
	}

	return client
}

// testRequest fabrique une requête OAuth gelée, telle que fosite en confie au
// magasin.
func testRequest(client fosite.Client, requestID, subject string) *fosite.Request {
	session := &fosite.DefaultSession{Subject: subject}
	session.SetExpiresAt(fosite.AccessToken, time.Now().Add(time.Hour))
	session.SetExpiresAt(fosite.RefreshToken, time.Now().Add(30*24*time.Hour))
	session.SetExpiresAt(fosite.AuthorizeCode, time.Now().Add(5*time.Minute))

	return &fosite.Request{
		ID:                requestID,
		RequestedAt:       time.Now().UTC().Truncate(time.Millisecond),
		Client:            client,
		RequestedScope:    fosite.Arguments{"mcp", "devis:read"},
		GrantedScope:      fosite.Arguments{"mcp"},
		RequestedAudience: fosite.Arguments{},
		GrantedAudience:   fosite.Arguments{},
		Form:              url.Values{"code_challenge": {"un-defi"}, "code_challenge_method": {"S256"}},
		Session:           session,
	}
}

// TestOAuthStoreClientRoundTrip vérifie qu'un client relu est celui qui a été
// écrit.
func TestOAuthStoreClientRoundTrip(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	written := testClient(t, store, "client-aller-retour")

	read, err := store.GetClient(t.Context(), written.ID)
	if err != nil {
		t.Fatalf("GetClient() échoué : %v", err)
	}

	if read.GetID() != written.ID {
		t.Errorf("GetID() = %q, attendu %q", read.GetID(), written.ID)
	}
	if !read.IsPublic() {
		t.Error("IsPublic() = false, le client a été enregistré comme public")
	}
	// Un client public n'a pas d'empreinte de secret : en rendre une ferait croire
	// à fosite qu'il peut s'authentifier.
	if len(read.GetHashedSecret()) != 0 {
		t.Errorf("GetHashedSecret() = %q, un client public n'en a pas", read.GetHashedSecret())
	}
	if !read.GetGrantTypes().Has("refresh_token") {
		t.Errorf("GetGrantTypes() = %v, doit contenir refresh_token", read.GetGrantTypes())
	}
	if got := read.GetRedirectURIs(); len(got) != 1 || got[0] != written.RedirectURIs[0] {
		t.Errorf("GetRedirectURIs() = %v, attendu %v", got, written.RedirectURIs)
	}

	// Le nom est exposé par l'extension facultative que la page de consentement
	// reconnaît par assertion de type.
	named, ok := read.(interface{ GetName() string })
	if !ok {
		t.Fatal("le client relu n'expose pas GetName : la page de consentement ne pourrait pas le nommer")
	}
	if named.GetName() != "Agent de test" {
		t.Errorf("GetName() = %q, attendu \"Agent de test\"", named.GetName())
	}
}

// TestOAuthStoreUnknownClient vérifie l'erreur attendue pour un client inconnu.
//
// fosite ne traduit en invalid_client que [fosite.ErrNotFound] ; toute autre
// erreur devient une erreur serveur, et l'appelant ne comprendrait pas qu'il
// s'est trompé d'identifiant.
func TestOAuthStoreUnknownClient(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)

	_, err := store.GetClient(t.Context(), "client-qui-nexiste-pas")
	if !errors.Is(err, fosite.ErrNotFound) {
		t.Fatalf("GetClient() = %v, attendu fosite.ErrNotFound", err)
	}
}

// TestOAuthStoreCountClients vérifie le comptage qui plafonne l'enregistrement.
func TestOAuthStoreCountClients(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)

	total, err := store.CountClients(t.Context())
	if err != nil {
		t.Fatalf("CountClients() échoué : %v", err)
	}
	if total != 0 {
		t.Fatalf("CountClients() = %d sur une base neuve, attendu 0", total)
	}

	testClient(t, store, "client-un")
	testClient(t, store, "client-deux")

	if total, err = store.CountClients(t.Context()); err != nil {
		t.Fatalf("CountClients() échoué : %v", err)
	}
	if total != 2 {
		t.Errorf("CountClients() = %d, attendu 2", total)
	}
}

// TestOAuthStoreAuthorizeCodeRoundTrip vérifie qu'une requête gelée revient
// entière.
//
// Le contenu du formulaire est ce qui compte le plus : c'est là que vivent
// code_challenge et code_challenge_method pour les enregistrements PKCE, et un
// formulaire perdu en route ferait échouer toute vérification PKCE avec une
// erreur qui ne dirait pas pourquoi.
func TestOAuthStoreAuthorizeCodeRoundTrip(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	client := testClient(t, store, "client-code")
	written := testRequest(client, "requete-1", "compte-1")

	if err := store.CreateAuthorizeCodeSession(t.Context(), "signature-du-code", written); err != nil {
		t.Fatalf("CreateAuthorizeCodeSession() échoué : %v", err)
	}

	session := new(fosite.DefaultSession)
	read, err := store.GetAuthorizeCodeSession(t.Context(), "signature-du-code", session)
	if err != nil {
		t.Fatalf("GetAuthorizeCodeSession() échoué : %v", err)
	}

	if read.GetID() != written.ID {
		t.Errorf("GetID() = %q, attendu %q", read.GetID(), written.ID)
	}
	if read.GetClient().GetID() != client.ID {
		t.Errorf("GetClient().GetID() = %q, attendu %q", read.GetClient().GetID(), client.ID)
	}
	if got := read.GetGrantedScopes(); !got.Has("mcp") {
		t.Errorf("GetGrantedScopes() = %v, doit contenir mcp", got)
	}
	if got := read.GetRequestedScopes(); !got.Has("devis:read") {
		t.Errorf("GetRequestedScopes() = %v, doit contenir devis:read", got)
	}
	if got := read.GetRequestForm().Get("code_challenge"); got != "un-defi" {
		t.Errorf("code_challenge = %q, attendu \"un-defi\" — PKCE ne pourrait pas être vérifié", got)
	}

	// La session est désérialisée dans le réceptacle fourni, et non dans un type
	// choisi par le magasin : c'est ce qui permet à l'appelant de récupérer la
	// sienne, avec ce qu'il y a mis.
	if session.GetSubject() != "compte-1" {
		t.Errorf("Subject = %q, attendu \"compte-1\"", session.GetSubject())
	}
	if read.GetSession() != fosite.Session(session) {
		t.Error("la session rendue n'est pas le réceptacle fourni")
	}
}

// TestOAuthStoreInvalidatedCodeKeepsRequest est le test le plus important du
// fichier.
//
// fosite exige qu'un code déjà consommé rende la requête **et**
// [fosite.ErrInvalidatedAuthorizeCode] : il se sert de cette requête pour
// révoquer tous les jetons issus de la même autorisation. Rendre nil ferait
// échouer la cascade sans faire échouer le cas heureux — la panne serait
// invisible jusqu'au jour où elle compterait.
func TestOAuthStoreInvalidatedCodeKeepsRequest(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	client := testClient(t, store, "client-rejeu")
	written := testRequest(client, "requete-2", "compte-2")

	if err := store.CreateAuthorizeCodeSession(t.Context(), "code-consomme", written); err != nil {
		t.Fatalf("CreateAuthorizeCodeSession() échoué : %v", err)
	}
	if err := store.InvalidateAuthorizeCodeSession(t.Context(), "code-consomme"); err != nil {
		t.Fatalf("InvalidateAuthorizeCodeSession() échoué : %v", err)
	}

	read, err := store.GetAuthorizeCodeSession(t.Context(), "code-consomme", new(fosite.DefaultSession))
	if !errors.Is(err, fosite.ErrInvalidatedAuthorizeCode) {
		t.Fatalf("erreur = %v, attendu fosite.ErrInvalidatedAuthorizeCode", err)
	}
	if read == nil {
		t.Fatal("requête nulle avec ErrInvalidatedAuthorizeCode : la révocation en cascade serait cassée")
	}
	if read.GetID() != written.ID {
		t.Errorf("GetID() = %q, attendu %q", read.GetID(), written.ID)
	}
}

// TestOAuthStoreUnknownCode vérifie qu'un code inventé se distingue d'un code
// consommé.
func TestOAuthStoreUnknownCode(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)

	_, err := store.GetAuthorizeCodeSession(t.Context(), "code-jamais-emis", new(fosite.DefaultSession))
	if !errors.Is(err, fosite.ErrNotFound) {
		t.Fatalf("erreur = %v, attendu fosite.ErrNotFound", err)
	}
}

// TestOAuthStoreInactiveRefreshTokenKeepsRequest est le pendant du test des
// codes, pour les jetons de rafraîchissement.
func TestOAuthStoreInactiveRefreshTokenKeepsRequest(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	client := testClient(t, store, "client-refresh")
	written := testRequest(client, "requete-3", "compte-3")

	if err := store.CreateRefreshTokenSession(t.Context(), "signature-refresh", "signature-acces", written); err != nil {
		t.Fatalf("CreateRefreshTokenSession() échoué : %v", err)
	}
	if err := store.RevokeRefreshToken(t.Context(), written.ID); err != nil {
		t.Fatalf("RevokeRefreshToken() échoué : %v", err)
	}

	read, err := store.GetRefreshTokenSession(t.Context(), "signature-refresh", new(fosite.DefaultSession))
	if !errors.Is(err, fosite.ErrInactiveToken) {
		t.Fatalf("erreur = %v, attendu fosite.ErrInactiveToken", err)
	}
	if read == nil {
		t.Fatal("requête nulle avec ErrInactiveToken : la détection de rejeu serait cassée")
	}
}

// TestOAuthStoreRotationRevokesFamily vérifie que la rotation retire de la
// circulation le jeton présenté et le jeton d'accès qui l'accompagnait.
func TestOAuthStoreRotationRevokesFamily(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	client := testClient(t, store, "client-rotation")
	written := testRequest(client, "requete-4", "compte-4")

	if err := store.CreateAccessTokenSession(t.Context(), "acces-1", written); err != nil {
		t.Fatalf("CreateAccessTokenSession() échoué : %v", err)
	}
	if err := store.CreateRefreshTokenSession(t.Context(), "refresh-1", "acces-1", written); err != nil {
		t.Fatalf("CreateRefreshTokenSession() échoué : %v", err)
	}

	if err := store.RotateRefreshToken(t.Context(), written.ID, "refresh-1"); err != nil {
		t.Fatalf("RotateRefreshToken() échoué : %v", err)
	}

	if _, err := store.GetRefreshTokenSession(t.Context(), "refresh-1", new(fosite.DefaultSession)); !errors.Is(err, fosite.ErrInactiveToken) {
		t.Errorf("le jeton de rafraîchissement présenté vaut encore : %v", err)
	}
	if _, err := store.GetAccessTokenSession(t.Context(), "acces-1", new(fosite.DefaultSession)); !errors.Is(err, fosite.ErrInactiveToken) {
		t.Errorf("le jeton d'accès de la famille vaut encore : %v", err)
	}

	// Le jeton émis après la rotation, lui, vit : la révocation porte sur ce qui
	// existait, pas sur l'identifiant de requête pour toujours.
	if err := store.CreateAccessTokenSession(t.Context(), "acces-2", written); err != nil {
		t.Fatalf("CreateAccessTokenSession() échoué : %v", err)
	}
	if _, err := store.GetAccessTokenSession(t.Context(), "acces-2", new(fosite.DefaultSession)); err != nil {
		t.Errorf("le jeton émis après la rotation est refusé : %v", err)
	}
}

// TestOAuthStoreRotationRefusesASecondPass vérifie qu'une famille déjà tournée
// ne tourne pas deux fois.
//
// C'est la version déterministe du test de concurrence qui suit, et elle vérifie
// la même chose : la rotation exige de désactiver un jeton *encore actif*. Sans
// cette condition, la seconde rotation réécrirait FALSE sur FALSE, se déclarerait
// satisfaite, et le rafraîchissement concurrent qui l'a déclenchée obtiendrait
// une paire de jetons de plus.
func TestOAuthStoreRotationRefusesASecondPass(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	client := testClient(t, store, "client-double-rotation")
	written := testRequest(client, "requete-double-rotation", "compte-9")

	if err := store.CreateRefreshTokenSession(t.Context(), "refresh-unique", "acces-unique", written); err != nil {
		t.Fatalf("CreateRefreshTokenSession() échoué : %v", err)
	}

	if err := store.RotateRefreshToken(t.Context(), written.ID, "refresh-unique"); err != nil {
		t.Fatalf("première rotation refusée : %v", err)
	}

	err := store.RotateRefreshToken(t.Context(), written.ID, "refresh-unique")
	if !errors.Is(err, fosite.ErrSerializationFailure) {
		t.Fatalf("seconde rotation = %v, attendu fosite.ErrSerializationFailure", err)
	}
}

// TestOAuthStoreRotationSerializesConcurrentRefresh est le test de la course.
//
// Il rejoue ce que fait fosite lorsqu'un même jeton de rafraîchissement est
// présenté par plusieurs requêtes à la fois : ouvrir une transaction, faire
// tourner la famille, écrire la paire de jetons neuve, valider. Les goroutines
// partent au même instant, sur la même famille — c'est exactement la fenêtre
// qu'une rotation non atomique laisse ouverte.
//
// L'attendu est qu'une seule aille au bout. Sans transaction, ou sans la
// condition « active = TRUE », toutes passeraient : chacune émettrait sa paire,
// et la détection de rejeu — la raison d'être de la rotation — serait contournée
// en changeant simplement l'ordre d'arrivée des requêtes.
func TestOAuthStoreRotationSerializesConcurrentRefresh(t *testing.T) {
	t.Parallel()

	const racers = 5

	store := newOAuthStore(t)
	client := testClient(t, store, "client-course")
	written := testRequest(client, "requete-course", "compte-10")

	if err := store.CreateAccessTokenSession(t.Context(), "acces-initial", written); err != nil {
		t.Fatalf("CreateAccessTokenSession() échoué : %v", err)
	}
	if err := store.CreateRefreshTokenSession(t.Context(), "refresh-initial", "acces-initial", written); err != nil {
		t.Fatalf("CreateRefreshTokenSession() échoué : %v", err)
	}

	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		results = make([]error, racers)
	)

	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start
			results[i] = refreshOnce(t.Context(), store, written, i)
		}()
	}

	close(start)
	wg.Wait()

	winners := 0
	for i, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, fosite.ErrSerializationFailure):
			// Le perdant attendu : la course est perdue proprement.
		default:
			t.Errorf("rafraîchissement %d = %v, attendu nil ou fosite.ErrSerializationFailure", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d rafraîchissements ont abouti, attendu exactement 1", winners)
	}

	// Le perdant ne laisse rien derrière lui : sa transaction est annulée en
	// entier, jetons neufs compris. C'est ce que la transaction achète en plus de
	// la sérialisation — sans elle, les paires des perdants existeraient malgré
	// leur erreur.
	survivors := 0
	for i := range racers {
		_, err := store.GetRefreshTokenSession(t.Context(), refreshSignature(i), new(fosite.DefaultSession))
		switch {
		case err == nil:
			survivors++
		case errors.Is(err, fosite.ErrNotFound):
			// La transaction annulée n'a rien écrit.
		default:
			t.Errorf("lecture du jeton %d = %v, attendu nil ou fosite.ErrNotFound", i, err)
		}
	}
	if survivors != 1 {
		t.Errorf("%d jetons de rafraîchissement neufs existent, attendu exactement 1", survivors)
	}

	// Le jeton présenté, lui, ne vaut plus rien pour personne.
	if _, err := store.GetRefreshTokenSession(t.Context(), "refresh-initial", new(fosite.DefaultSession)); !errors.Is(err, fosite.ErrInactiveToken) {
		t.Errorf("le jeton présenté = %v, attendu fosite.ErrInactiveToken", err)
	}
}

// refreshSignature nomme les jetons d'un concurrent de la course.
func refreshSignature(racer int) string {
	return fmt.Sprintf("refresh-course-%d", racer)
}

// refreshOnce rejoue la séquence exacte de fosite au point de terminaison de
// jeton : transaction, rotation, écriture de la paire neuve, validation.
//
// L'annulation en cas d'erreur reproduit elle aussi fosite, qui annule dans un
// defer sur tout chemin d'échec. C'est ce qui fait de ce test un test du magasin
// et non d'un scénario inventé.
func refreshOnce(ctx context.Context, store *postgres.OAuthStore, request *fosite.Request, racer int) (err error) {
	ctx, err = store.BeginTX(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if rollbackErr := store.Rollback(ctx); rollbackErr != nil {
				err = fmt.Errorf("%w (annulation : %w)", err, rollbackErr)
			}
		}
	}()

	if err := store.RotateRefreshToken(ctx, request.ID, "refresh-initial"); err != nil {
		return err
	}

	access := fmt.Sprintf("acces-course-%d", racer)
	if err := store.CreateAccessTokenSession(ctx, access, request); err != nil {
		return err
	}
	if err := store.CreateRefreshTokenSession(ctx, refreshSignature(racer), access, request); err != nil {
		return err
	}

	return store.Commit(ctx)
}

// TestOAuthStoreTransactionRollsBack vérifie que la transaction porte réellement
// les écritures, et qu'une annulation les emporte.
//
// C'est le test qui garde le mécanisme du contexte honnête : une méthode qui
// oublierait de choisir son querier écrirait sur le pool, donc hors transaction.
// L'écriture serait visible avant le commit et survivrait au rollback — sans
// qu'aucun test du protocole ne s'en aperçoive.
func TestOAuthStoreTransactionRollsBack(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	client := testClient(t, store, "client-transaction")
	written := testRequest(client, "requete-transaction", "compte-11")

	ctx, err := store.BeginTX(t.Context())
	if err != nil {
		t.Fatalf("BeginTX() échoué : %v", err)
	}

	if err := store.CreateAccessTokenSession(ctx, "acces-annule", written); err != nil {
		t.Fatalf("CreateAccessTokenSession() échoué : %v", err)
	}

	// Dans la transaction, l'écriture existe.
	if _, err := store.GetAccessTokenSession(ctx, "acces-annule", new(fosite.DefaultSession)); err != nil {
		t.Fatalf("le jeton écrit n'est pas lisible dans sa propre transaction : %v", err)
	}

	// Une transaction déjà ouverte ne se rouvre pas : fosite ne les imbrique
	// pas, et une réouverture silencieuse validerait la première trop tôt.
	if _, err := store.BeginTX(ctx); err == nil {
		t.Error("BeginTX() a accepté d'imbriquer une transaction")
	}

	if err := store.Rollback(ctx); err != nil {
		t.Fatalf("Rollback() échoué : %v", err)
	}

	if _, err := store.GetAccessTokenSession(t.Context(), "acces-annule", new(fosite.DefaultSession)); !errors.Is(err, fosite.ErrNotFound) {
		t.Errorf("erreur = %v, attendu fosite.ErrNotFound après annulation", err)
	}
}

// TestOAuthStoreTransactionRefusesStrayCommit vérifie qu'on ne valide ni
// n'annule une transaction qui n'a pas été ouverte.
//
// Le contexte est le véhicule de la transaction : un contexte sans transaction
// qui se ferait valider en silence signalerait un chemin de code ayant perdu le
// sien, c'est-à-dire des écritures parties sur le pool sans que rien ne le dise.
func TestOAuthStoreTransactionRefusesStrayCommit(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)

	if err := store.Commit(t.Context()); err == nil {
		t.Error("Commit() sans transaction ouverte = nil")
	}
	if err := store.Rollback(t.Context()); err == nil {
		t.Error("Rollback() sans transaction ouverte = nil")
	}
}

// TestOAuthStoreGrants vérifie la mémoire des consentements.
//
// Elle n'autorise rien : elle alimente l'indicateur « première autorisation » de
// la page de consentement, seul repère qui distingue l'agent employé depuis des
// mois d'un homonyme enregistré ce matin. Une écriture qui écraserait la
// première date, ou une lecture qui oublierait un consentement ancien, rendraient
// ce repère faux dans le sens rassurant.
func TestOAuthStoreGrants(t *testing.T) {
	t.Parallel()

	store, userID := oauthStoreWithAccount(t)
	testClient(t, store, "client-consentement")
	other := string(userID[:len(userID)-1]) + "0"

	granted, err := store.HasGrant(t.Context(), string(userID), "client-consentement")
	if err != nil {
		t.Fatalf("HasGrant() échoué : %v", err)
	}
	if granted {
		t.Fatal("HasGrant() = true avant tout consentement")
	}

	first := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	if recordErr := store.RecordGrant(t.Context(), string(userID), "client-consentement", first); recordErr != nil {
		t.Fatalf("RecordGrant() échoué : %v", recordErr)
	}

	if granted, err = store.HasGrant(t.Context(), string(userID), "client-consentement"); err != nil {
		t.Fatalf("HasGrant() échoué : %v", err)
	}
	if !granted {
		t.Error("HasGrant() = false après un consentement")
	}

	// Un second consentement ne doit ni échouer ni réécrire la date : c'est
	// l'ancienneté de la relation qui a valeur de signal.
	if recordErr := store.RecordGrant(t.Context(), string(userID), "client-consentement", first.Add(24*time.Hour)); recordErr != nil {
		t.Fatalf("second RecordGrant() échoué : %v", recordErr)
	}

	// Un autre compte n'hérite de rien : le consentement est celui d'une
	// personne, pas de l'instance.
	if granted, err = store.HasGrant(t.Context(), other, "client-consentement"); err != nil {
		t.Fatalf("HasGrant() sur un autre compte échoué : %v", err)
	}
	if granted {
		t.Error("HasGrant() = true pour un compte qui n'a rien consenti")
	}
}

// TestOAuthStoreGrantRejectsUnknownAccount vérifie que la table refuse un
// consentement rattaché à un compte inexistant.
func TestOAuthStoreGrantRejectsUnknownAccount(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	testClient(t, store, "client-sans-compte")

	err := store.RecordGrant(t.Context(), "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "client-sans-compte", time.Now())
	if err == nil {
		t.Error("un consentement a été enregistré pour un compte inexistant")
	}
}

// TestOAuthStoreDeleteAndRevokeTolerateAbsence vérifie que le ménage ne se
// plaint pas de ne rien trouver.
//
// fosite révoque par précaution des familles qui n'ont parfois jamais eu de
// jeton d'un type donné. Transformer cela en échec ferait rater la révocation
// des autres, et une révocation partielle est pire que pas de révocation du
// tout : elle laisse croire que le travail est fait.
func TestOAuthStoreDeleteAndRevokeTolerateAbsence(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)

	for name, call := range map[string]func() error{
		"DeleteAccessTokenSession":  func() error { return store.DeleteAccessTokenSession(t.Context(), "inconnue") },
		"DeleteRefreshTokenSession": func() error { return store.DeleteRefreshTokenSession(t.Context(), "inconnue") },
		"DeletePKCERequestSession":  func() error { return store.DeletePKCERequestSession(t.Context(), "inconnue") },
		"RevokeAccessToken":         func() error { return store.RevokeAccessToken(t.Context(), "requete-inconnue") },
		"RevokeRefreshToken":        func() error { return store.RevokeRefreshToken(t.Context(), "requete-inconnue") },
	} {
		if err := call(); err != nil {
			t.Errorf("%s() sur un enregistrement absent = %v, attendu nil", name, err)
		}
	}
}

// TestOAuthStorePKCERoundTrip vérifie le magasin PKCE.
func TestOAuthStorePKCERoundTrip(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	client := testClient(t, store, "client-pkce")
	written := testRequest(client, "requete-5", "compte-5")

	if err := store.CreatePKCERequestSession(t.Context(), "signature-pkce", written); err != nil {
		t.Fatalf("CreatePKCERequestSession() échoué : %v", err)
	}

	read, err := store.GetPKCERequestSession(t.Context(), "signature-pkce", new(fosite.DefaultSession))
	if err != nil {
		t.Fatalf("GetPKCERequestSession() échoué : %v", err)
	}
	if got := read.GetRequestForm().Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, attendu S256", got)
	}

	// Le défi ne sert qu'une fois : il est effacé, pas désactivé.
	if err := store.DeletePKCERequestSession(t.Context(), "signature-pkce"); err != nil {
		t.Fatalf("DeletePKCERequestSession() échoué : %v", err)
	}
	if _, err := store.GetPKCERequestSession(t.Context(), "signature-pkce", new(fosite.DefaultSession)); !errors.Is(err, fosite.ErrNotFound) {
		t.Errorf("erreur = %v, attendu fosite.ErrNotFound après suppression", err)
	}
}

// TestOAuthStoreAssertionJWTRefused vérifie que l'authentification par assertion
// JWT est refusée.
//
// Avanti ne la propose pas. Ce qui compte est le sens du refus : accepter
// reviendrait à déclarer « ce JTI n'a jamais servi », donc à autoriser le rejeu
// d'une assertion si la méthode venait à être activée sans que ces deux
// fonctions soient réécrites.
func TestOAuthStoreAssertionJWTRefused(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)

	if err := store.ClientAssertionJWTValid(t.Context(), "un-jti"); err == nil {
		t.Error("ClientAssertionJWTValid() = nil, ce qui autoriserait le rejeu d'une assertion")
	}
	if err := store.SetClientAssertionJWT(t.Context(), "un-jti", time.Now().Add(time.Hour)); err == nil {
		t.Error("SetClientAssertionJWT() = nil, alors que la méthode n'est pas prise en charge")
	}
}

// TestOAuthStorePurgeExpired vérifie le ménage.
//
// Sans lui, la table ne ferait que grandir : chaque rafraîchissement y laisse
// deux lignes mortes.
func TestOAuthStorePurgeExpired(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)
	client := testClient(t, store, "client-purge")

	// Un jeton périmé depuis longtemps.
	expired := testRequest(client, "requete-perimee", "compte-6")
	expired.Session.SetExpiresAt(fosite.AccessToken, time.Now().Add(-48*time.Hour))
	if err := store.CreateAccessTokenSession(t.Context(), "acces-perime", expired); err != nil {
		t.Fatalf("CreateAccessTokenSession() échoué : %v", err)
	}

	// Un jeton encore valide.
	alive := testRequest(client, "requete-vivante", "compte-7")
	if err := store.CreateAccessTokenSession(t.Context(), "acces-vivant", alive); err != nil {
		t.Fatalf("CreateAccessTokenSession() échoué : %v", err)
	}

	removed, err := store.PurgeExpired(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("PurgeExpired() échoué : %v", err)
	}
	if removed != 1 {
		t.Errorf("PurgeExpired() = %d, attendu 1", removed)
	}

	if _, err := store.GetAccessTokenSession(t.Context(), "acces-perime", new(fosite.DefaultSession)); !errors.Is(err, fosite.ErrNotFound) {
		t.Errorf("le jeton périmé est encore là : %v", err)
	}
	if _, err := store.GetAccessTokenSession(t.Context(), "acces-vivant", new(fosite.DefaultSession)); err != nil {
		t.Errorf("la purge a emporté un jeton encore valide : %v", err)
	}
}

// TestOAuthStoreRejectsUnknownClient vérifie que la base refuse un jeton
// rattaché à un client inexistant.
//
// La clé étrangère est ce qui garantit qu'un client supprimé emporte ses jetons
// : sans elle, des jetons orphelins resteraient vérifiables.
func TestOAuthStoreRejectsUnknownClient(t *testing.T) {
	t.Parallel()

	store := newOAuthStore(t)

	orphan := testRequest(&fosite.DefaultClient{ID: "client-fantome"}, "requete-orpheline", "compte-8")

	if err := store.CreateAccessTokenSession(t.Context(), "acces-orphelin", orphan); err == nil {
		t.Error("un jeton a été enregistré pour un client inexistant")
	}
}
