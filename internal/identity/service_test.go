package identity_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

func TestNewAccountServiceRejectsMissingPort(t *testing.T) {
	t.Parallel()

	cases := map[string]identity.ServiceOptions{
		"sans dépôt":   {Hasher: &plainHasher{}},
		"sans hacheur": {Repo: newRepo()},
		"sans rien":    {},
	}

	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := identity.NewAccountService(opts); err == nil {
				t.Error("NewAccountService() doit refuser un port manquant")
			}
		})
	}
}

func TestCreateAccount(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	user, err := h.service.Create(t.Context(), "  Romain@Exemple.FR ", "  Romain   Badino ", validPassword, identity.RoleProprietaire)
	if err != nil {
		t.Fatalf("Create() échoué : %v", err)
	}

	if user.Email != "romain@exemple.fr" {
		t.Errorf("Email = %q, l'adresse doit être normalisée", user.Email)
	}
	if user.DisplayName != "Romain Badino" {
		t.Errorf("DisplayName = %q, les espaces doivent être réduits", user.DisplayName)
	}
	if user.Role != identity.RoleProprietaire {
		t.Errorf("Role = %q", user.Role)
	}
	if !user.Active {
		t.Error("un compte créé doit être actif")
	}
	if user.ID == "" {
		t.Error("un compte créé doit porter un identifiant")
	}
	if !user.CreatedAt.Equal(frozenClock) || !user.UpdatedAt.Equal(frozenClock) {
		t.Errorf("horodatages = (%s, %s), attendu %s pour les deux", user.CreatedAt, user.UpdatedAt, frozenClock)
	}
	// L'identifiant vient bien du générateur injecté : c'est ce qui rend les
	// horodatages et les identifiants déterministes dans toute cette suite.
	if user.ID != "compte-01" {
		t.Errorf("ID = %q, le générateur injecté devait rendre compte-01", user.ID)
	}
}

// TestNewAccountServiceFallsBackToDefaults : horloge et générateur
// d'identifiants sont optionnels. Sans eux, le service prend l'heure réelle et
// des UUID — c'est ce que fait cmd/avanti, qui n'en injecte aucun.
func TestNewAccountServiceFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	repo := newRepo()
	service, err := identity.NewAccountService(identity.ServiceOptions{
		Repo:   repo,
		Hasher: &plainHasher{},
	})
	if err != nil {
		t.Fatalf("NewAccountService() échoué : %v", err)
	}

	before := time.Now()
	user, err := service.Create(t.Context(), "romain@exemple.fr", "Romain", validPassword, identity.RoleProprietaire)
	if err != nil {
		t.Fatalf("Create() échoué : %v", err)
	}

	if len(user.ID) != 36 {
		t.Errorf("ID = %q, un UUID de 36 caractères était attendu du générateur par défaut", user.ID)
	}
	if user.CreatedAt.Before(before.Add(-time.Minute)) || user.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("CreatedAt = %s, l'horloge par défaut devait donner l'heure courante", user.CreatedAt)
	}
	if user.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt est en %s, les horodatages sont stockés en UTC", user.CreatedAt.Location())
	}
}

// TestCreateNeverStoresPlaintextPassword est la vérification qui compte le
// plus sur ce chemin : l'empreinte ne doit pas contenir le mot de passe.
func TestCreateNeverStoresPlaintextPassword(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	if user.PasswordHash.Empty() {
		t.Fatal("le compte créé n'a pas d'empreinte")
	}
	if string(user.PasswordHash) == validPassword {
		t.Fatal("l'empreinte est le mot de passe en clair")
	}
	// L'empreinte du hacheur trivial contient le mot de passe par construction :
	// c'est ce qui la rend inutilisable en production, et c'est pourquoi le test
	// se contente ici de vérifier qu'un hachage a bien eu lieu. La propriété
	// réelle est vérifiée par TestArgon2idHasher.
	if !strings.HasPrefix(string(user.PasswordHash), "trivial:") {
		t.Errorf("PasswordHash = %q : l'empreinte ne vient pas du hacheur injecté", string(user.PasswordHash))
	}
}

// TestPasswordHashNeverPrints : un « %v » sur un compte ne doit pas recopier
// l'empreinte dans un journal.
func TestPasswordHashNeverPrints(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	shown := render(user)
	if strings.Contains(shown, string(user.PasswordHash)) {
		t.Errorf("l'affichage d'un User contient son empreinte : %s", shown)
	}
	if !strings.Contains(shown, "empreinte masquée") {
		t.Errorf("l'affichage d'un User devrait porter la mention de masquage : %s", shown)
	}

	if got := identity.PasswordHash("").String(); !strings.Contains(got, "aucune") {
		t.Errorf("PasswordHash(\"\").String() = %q", got)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		email       string
		displayName string
		password    string
		role        identity.Role
		want        error
	}{
		{
			name: "email invalide", email: "pas-un-email", displayName: "Romain",
			password: validPassword, role: identity.RoleProprietaire,
			want: identity.ErrInvalidEmail,
		},
		{
			name: "nom d'affichage vide", email: "romain@exemple.fr", displayName: "  ",
			password: validPassword, role: identity.RoleProprietaire,
			want: identity.ErrEmptyDisplayName,
		},
		{
			name: "rôle inconnu", email: "romain@exemple.fr", displayName: "Romain",
			password: validPassword, role: identity.Role("administrateur"),
			want: identity.ErrUnknownRole,
		},
		{
			name: "rôle vide", email: "romain@exemple.fr", displayName: "Romain",
			password: validPassword, role: identity.Role(""),
			want: identity.ErrUnknownRole,
		},
		{
			name: "mot de passe trop court", email: "romain@exemple.fr", displayName: "Romain",
			password: "onze-carac", role: identity.RoleProprietaire,
			want: identity.ErrPasswordTooShort,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := h.service.Create(t.Context(), tc.email, tc.displayName, tc.password, tc.role)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Create() = %v, attendu %v", err, tc.want)
			}

			accounts, err := h.service.List(t.Context())
			if err != nil {
				t.Fatalf("List() échoué : %v", err)
			}
			if len(accounts) != 0 {
				t.Errorf("une création refusée a tout de même écrit %d compte(s)", len(accounts))
			}
		})
	}
}

// TestCreateDoesNotHashBeforeValidation : refuser un rôle inconnu ne doit pas
// coûter un argon2id. Le test compte les appels au hacheur plutôt que le temps.
func TestCreateDoesNotHashBeforeValidation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	before := h.hasher.verifies.Load()

	if _, err := h.service.Create(t.Context(), "romain@exemple.fr", "Romain", validPassword, identity.Role("admin")); err == nil {
		t.Fatal("Create() doit refuser un rôle inconnu")
	}

	if h.hasher.verifies.Load() != before {
		t.Error("une création refusée a fait travailler le hacheur")
	}
}

// TestCreateRejectsTakenEmail vérifie que l'unicité passe par la
// normalisation : deux écritures différentes de la même adresse sont la même.
func TestCreateRejectsTakenEmail(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	for _, duplicate := range []string{"romain@exemple.fr", "Romain@Exemple.FR", "  ROMAIN@EXEMPLE.FR  "} {
		_, err := h.service.Create(t.Context(), duplicate, "Autre", validPassword, identity.RoleCollaborateur)
		if !errors.Is(err, identity.ErrEmailTaken) {
			t.Errorf("Create(%q) = %v, attendu ErrEmailTaken", duplicate, err)
		}
	}
}

func TestAuthenticateSucceeds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	// La casse et les espaces de saisie ne doivent pas empêcher la connexion.
	actor, err := h.service.Authenticate(t.Context(), "  Romain@Exemple.FR ", validPassword)
	if err != nil {
		t.Fatalf("Authenticate() échoué : %v", err)
	}

	if actor.UserID() != user.ID {
		t.Errorf("UserID() = %q, attendu %q", actor.UserID(), user.ID)
	}
	if actor.Role() != identity.RoleProprietaire {
		t.Errorf("Role() = %q", actor.Role())
	}
	if actor.Anonymous() {
		t.Error("un acteur authentifié ne doit pas être anonyme")
	}
	if !actor.Allows(identity.ScopeMCP) {
		t.Error("le propriétaire authentifié doit détenir le scope mcp")
	}
}

// TestAuthenticateIsIndistinguishable est le test de fond du chemin de connexion :
// email inconnu, email malformé et mauvais mot de passe rendent tous la même
// erreur, et chacun fait payer exactement une vérification d'empreinte.
//
// Compter les vérifications, plutôt que chronométrer, donne un test stable : sur
// une machine de CI partagée, une mesure de durée serait bruitée au point d'être
// soit inutile, soit intermittente.
func TestAuthenticateIsIndistinguishable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		email    string
		password string
	}{
		{name: "email inconnu", email: "inconnu@exemple.fr", password: validPassword},
		{name: "email malformé", email: "pas-un-email", password: validPassword},
		{name: "email vide", email: "", password: validPassword},
		{name: "mauvais mot de passe", email: "romain@exemple.fr", password: "un autre mot de passe"},
		{name: "mot de passe vide", email: "romain@exemple.fr", password: ""},
		{name: "casse du mot de passe", email: "romain@exemple.fr", password: strings.ToUpper(validPassword)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

			before := h.hasher.verifies.Load()

			actor, err := h.service.Authenticate(t.Context(), tc.email, tc.password)
			if !errors.Is(err, identity.ErrInvalidCredentials) {
				t.Fatalf("Authenticate() = %v, attendu ErrInvalidCredentials", err)
			}
			if !actor.Anonymous() {
				t.Error("un échec d'authentification doit rendre un acteur anonyme")
			}

			if verifies := h.hasher.verifies.Load() - before; verifies != 1 {
				t.Errorf("%d vérification(s) d'empreinte, attendu 1 — le coût doit être le même quel que soit le motif du refus", verifies)
			}
		})
	}
}

// TestAuthenticateRejectsDisabledAccount : le mot de passe est vérifié d'abord,
// ce qui fait que la désactivation n'est révélée qu'à qui connaît déjà le secret.
func TestAuthenticateRejectsDisabledAccount(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	if err := h.service.Deactivate(t.Context(), user.ID); err != nil {
		t.Fatalf("Deactivate() échoué : %v", err)
	}

	actor, err := h.service.Authenticate(t.Context(), "romain@exemple.fr", validPassword)
	if !errors.Is(err, identity.ErrAccountDisabled) {
		t.Fatalf("Authenticate() = %v, attendu ErrAccountDisabled", err)
	}
	if !actor.Anonymous() {
		t.Error("un compte désactivé ne doit pas rendre d'acteur")
	}

	// Avec un mauvais mot de passe, en revanche, rien ne doit filtrer de
	// l'existence ni de l'état du compte.
	if _, err := h.service.Authenticate(t.Context(), "romain@exemple.fr", "mauvais mot de passe"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("Authenticate() = %v, un mauvais mot de passe sur un compte désactivé doit rendre ErrInvalidCredentials", err)
	}
}

// TestAuthenticateDoesNotHideAFailure : une base injoignable est un incident à
// signaler, pas un refus d'identifiants. Les confondre ferait qu'une panne
// s'afficherait à l'utilisateur comme une faute de frappe de sa part.
func TestAuthenticateDoesNotHideAFailure(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)
	h.repo.failing = true

	_, err := h.service.Authenticate(t.Context(), "romain@exemple.fr", validPassword)
	if !errors.Is(err, errFailure) {
		t.Fatalf("Authenticate() = %v, attendu la panne du dépôt", err)
	}
	if errors.Is(err, identity.ErrInvalidCredentials) {
		t.Error("une panne de persistance ne doit pas se déguiser en identifiants invalides")
	}
}

// TestAuthenticateRejectsUnreadableHash : une empreinte corrompue en base
// est une anomalie, pas un mot de passe erroné.
func TestAuthenticateRejectsUnreadableHash(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)
	h.hasher.unreadable = map[identity.PasswordHash]bool{user.PasswordHash: true}

	_, err := h.service.Authenticate(t.Context(), "romain@exemple.fr", validPassword)
	if err == nil {
		t.Fatal("Authenticate() doit échouer sur une empreinte illisible")
	}
	if errors.Is(err, identity.ErrInvalidCredentials) {
		t.Error("une empreinte corrompue ne doit pas se déguiser en identifiants invalides")
	}
}

func TestResetPassword(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	const newPassword = "une toute nouvelle phrase de passe"

	// L'ancien mot de passe n'est PAS demandé : c'est une réinitialisation
	// d'administration, celle du mot de passe perdu.
	h.instant = frozenClock.Add(48 * time.Hour)
	if err := h.service.ResetPassword(t.Context(), user.ID, newPassword); err != nil {
		t.Fatalf("ResetPassword() échoué : %v", err)
	}

	if _, err := h.service.Authenticate(t.Context(), "romain@exemple.fr", newPassword); err != nil {
		t.Errorf("le nouveau mot de passe est refusé : %v", err)
	}
	if _, err := h.service.Authenticate(t.Context(), "romain@exemple.fr", validPassword); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("l'ancien mot de passe fonctionne encore : %v", err)
	}

	after, err := h.service.ByID(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	if !after.UpdatedAt.After(user.UpdatedAt) {
		t.Errorf("UpdatedAt = %s, il devrait avoir avancé depuis %s", after.UpdatedAt, user.UpdatedAt)
	}
	if !after.CreatedAt.Equal(user.CreatedAt) {
		t.Errorf("CreatedAt a changé : %s puis %s", user.CreatedAt, after.CreatedAt)
	}
}

// TestResetPasswordKeepsThePolicy : la réinitialisation suit la même politique
// de robustesse que la création — pas de porte dérobée vers un mot de passe
// faible — et un refus ne modifie rien.
func TestResetPasswordKeepsThePolicy(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	if err := h.service.ResetPassword(t.Context(), user.ID, "trop court"); !errors.Is(err, identity.ErrPasswordTooShort) {
		t.Fatalf("ResetPassword(trop court) = %v, attendu ErrPasswordTooShort", err)
	}

	// Le mot de passe d'origine doit toujours fonctionner : une
	// réinitialisation refusée ne laisse pas le compte à moitié modifié.
	if _, err := h.service.Authenticate(t.Context(), "romain@exemple.fr", validPassword); err != nil {
		t.Errorf("une réinitialisation refusée a tout de même modifié le compte : %v", err)
	}
}

func TestResetPasswordOnUnknownAccount(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	err := h.service.ResetPassword(t.Context(), identity.ID("fantome"), "une nouvelle phrase de passe")
	if !errors.Is(err, identity.ErrUnknownUser) {
		t.Errorf("ResetPassword() = %v, attendu ErrUnknownUser", err)
	}
}

func TestChangeRole(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleCollaborateur)

	h.instant = frozenClock.Add(48 * time.Hour)
	if err := h.service.ChangeRole(t.Context(), user.ID, identity.RoleProprietaire); err != nil {
		t.Fatalf("ChangeRole() échoué : %v", err)
	}

	after, err := h.service.ByID(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	if after.Role != identity.RoleProprietaire {
		t.Errorf("Role = %q, attendu %q", after.Role, identity.RoleProprietaire)
	}
	if !after.UpdatedAt.After(user.UpdatedAt) {
		t.Errorf("UpdatedAt = %s, il devrait avoir avancé depuis %s", after.UpdatedAt, user.UpdatedAt)
	}

	// L'effet sur les droits est celui de la table des rôles : le nouveau rôle
	// porte le scope mcp, l'ancien ne le portait pas.
	actor, err := h.service.Authenticate(t.Context(), "romain@exemple.fr", validPassword)
	if err != nil {
		t.Fatalf("Authenticate() échoué : %v", err)
	}
	if !actor.Allows(identity.ScopeMCP) {
		t.Error("le compte promu proprietaire doit porter le scope mcp")
	}
}

// TestChangeRoleIsIdempotent : redonner son rôle à un compte ne change rien,
// pas même la date de modification — il n'y a pas eu de modification.
func TestChangeRoleIsIdempotent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	h.instant = frozenClock.Add(48 * time.Hour)
	if err := h.service.ChangeRole(t.Context(), user.ID, identity.RoleProprietaire); err != nil {
		t.Fatalf("ChangeRole(même rôle) échoué : %v", err)
	}

	after, err := h.service.ByID(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	if !after.UpdatedAt.Equal(user.UpdatedAt) {
		t.Errorf("UpdatedAt a bougé (%s → %s) alors que rien n'a changé", user.UpdatedAt, after.UpdatedAt)
	}
}

func TestChangeRoleRejectsUnknownRole(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	err := h.service.ChangeRole(t.Context(), user.ID, identity.Role("administrateur"))
	if !errors.Is(err, identity.ErrUnknownRole) {
		t.Fatalf("ChangeRole(rôle inconnu) = %v, attendu ErrUnknownRole", err)
	}

	after, err := h.service.ByID(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	if after.Role != identity.RoleProprietaire {
		t.Errorf("Role = %q, un refus ne doit rien modifier", after.Role)
	}
}

func TestDeactivateAndReactivate(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	h.instant = frozenClock.Add(time.Hour)
	if err := h.service.Deactivate(t.Context(), user.ID); err != nil {
		t.Fatalf("Deactivate() échoué : %v", err)
	}

	disabled, err := h.service.ByID(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	if disabled.Active {
		t.Error("le compte devrait être désactivé")
	}
	// Un compte désactivé ne donne aucun droit, même si un appel oubliait de
	// vérifier Active avant de construire l'acteur.
	if !disabled.Actor().Anonymous() {
		t.Error("Actor() d'un compte désactivé doit être anonyme")
	}
	if disabled.Actor().Allows(identity.ScopeDevisRead) {
		t.Error("un compte désactivé ne doit détenir aucun scope")
	}

	if err := h.service.Reactivate(t.Context(), user.ID); err != nil {
		t.Fatalf("Reactivate() échoué : %v", err)
	}
	if _, err := h.service.Authenticate(t.Context(), "romain@exemple.fr", validPassword); err != nil {
		t.Errorf("le compte réactivé ne peut pas se connecter : %v", err)
	}
}

// TestDeactivateIsIdempotent : rejouer l'opération ne change rien et ne se
// plaint pas — la propriété qu'attend une commande d'exploitation relancée.
func TestDeactivateIsIdempotent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	user := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	for range 3 {
		if err := h.service.Deactivate(t.Context(), user.ID); err != nil {
			t.Fatalf("Deactivate() échoué : %v", err)
		}
	}

	after, err := h.service.ByID(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	if after.Active {
		t.Error("le compte devrait être désactivé")
	}

	for range 3 {
		if err := h.service.Reactivate(t.Context(), user.ID); err != nil {
			t.Fatalf("Reactivate() échoué : %v", err)
		}
	}
}

func TestDeactivateUnknownAccount(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	if err := h.service.Deactivate(t.Context(), identity.ID("fantome")); !errors.Is(err, identity.ErrUnknownUser) {
		t.Errorf("Deactivate() = %v, attendu ErrUnknownUser", err)
	}
	if err := h.service.Reactivate(t.Context(), identity.ID("fantome")); !errors.Is(err, identity.ErrUnknownUser) {
		t.Errorf("Reactivate() = %v, attendu ErrUnknownUser", err)
	}
}

// TestByEmailNormalizesInput : la ligne de commande désigne un compte par son
// adresse telle qu'on la retape, casse et espaces compris.
func TestByEmailNormalizesInput(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	created := h.createAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	for _, input := range []string{"romain@exemple.fr", "Romain@Exemple.FR", "  romain@exemple.fr  "} {
		found, err := h.service.ByEmail(t.Context(), input)
		if err != nil {
			t.Errorf("ByEmail(%q) échoué : %v", input, err)
			continue
		}
		if found.ID != created.ID {
			t.Errorf("ByEmail(%q) rend %q, attendu %q", input, found.ID, created.ID)
		}
	}

	if _, err := h.service.ByEmail(t.Context(), "inconnu@exemple.fr"); !errors.Is(err, identity.ErrUnknownUser) {
		t.Errorf("ByEmail() = %v sur une adresse inconnue, attendu ErrUnknownUser", err)
	}
	if _, err := h.service.ByEmail(t.Context(), "pas-un-email"); !errors.Is(err, identity.ErrInvalidEmail) {
		t.Errorf("ByEmail() = %v sur une adresse malformée, attendu ErrInvalidEmail", err)
	}
}

func TestListSortsByEmail(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	for _, email := range []string{"zoe@exemple.fr", "amelie@exemple.fr", "romain@exemple.fr"} {
		h.createAccount(t, email, identity.RoleCollaborateur)
	}

	accounts, err := h.service.List(t.Context())
	if err != nil {
		t.Fatalf("List() échoué : %v", err)
	}

	wants := []string{"amelie@exemple.fr", "romain@exemple.fr", "zoe@exemple.fr"}
	if len(accounts) != len(wants) {
		t.Fatalf("List() a rendu %d comptes, attendu %d", len(accounts), len(wants))
	}
	for i, want := range wants {
		if accounts[i].Email != want {
			t.Errorf("comptes[%d].Email = %q, attendu %q", i, accounts[i].Email, want)
		}
	}
}
