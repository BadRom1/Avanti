package postgres_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// TestCreateThenRead est le test d'aller-retour : ce qui ressort de la base doit
// être ce qui y est entré, y compris les types que PostgreSQL manipule
// différemment de Go — l'uuid de l'identifiant et l'instant des horodatages.
func TestCreateThenRead(t *testing.T) {
	t.Parallel()

	repo := newRepo(t)
	want := testAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	if err := repo.Create(t.Context(), want); err != nil {
		t.Fatalf("Create() échoué : %v", err)
	}

	for _, tc := range []struct {
		name string
		read func() (identity.User, error)
	}{
		{name: "par email", read: func() (identity.User, error) {
			return repo.ByEmail(t.Context(), want.Email)
		}},
		{name: "par identifiant", read: func() (identity.User, error) {
			return repo.ByID(t.Context(), want.ID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.read()
			if err != nil {
				t.Fatalf("lecture échouée : %v", err)
			}
			compareAccounts(t, got, want)
		})
	}
}

// compareAccounts vérifie l'égalité champ par champ. Les horodatages sont
// comparés avec Equal et non == : PostgreSQL les rend dans le fuseau de la
// session, ce qui change la représentation sans changer l'instant.
func compareAccounts(t *testing.T, got, want identity.User) {
	t.Helper()

	if got.ID != want.ID {
		t.Errorf("ID = %q, attendu %q", got.ID, want.ID)
	}
	if got.Email != want.Email {
		t.Errorf("Email = %q, attendu %q", got.Email, want.Email)
	}
	if got.DisplayName != want.DisplayName {
		t.Errorf("DisplayName = %q, attendu %q", got.DisplayName, want.DisplayName)
	}
	if got.PasswordHash != want.PasswordHash {
		t.Error("l'empreinte relue diffère de celle qui a été écrite")
	}
	if got.Role != want.Role {
		t.Errorf("Role = %q, attendu %q", got.Role, want.Role)
	}
	if got.Active != want.Active {
		t.Errorf("Active = %t, attendu %t", got.Active, want.Active)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %s, attendu %s", got.CreatedAt, want.CreatedAt)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt = %s, attendu %s", got.UpdatedAt, want.UpdatedAt)
	}
}

// TestReadsWithoutResult : le port promet [identity.ErrUnknownUser], et
// c'est de cette promesse que dépend l'indistinguabilité de l'authentification.
func TestReadsWithoutResult(t *testing.T) {
	t.Parallel()

	repo := newRepo(t)

	if _, err := repo.ByEmail(t.Context(), "personne@exemple.fr"); !errors.Is(err, identity.ErrUnknownUser) {
		t.Errorf("ByEmail() = %v, attendu ErrUnknownUser", err)
	}

	missing := testAccount(t, "absent@exemple.fr", identity.RoleProprietaire)
	if _, err := repo.ByID(t.Context(), missing.ID); !errors.Is(err, identity.ErrUnknownUser) {
		t.Errorf("ByID() = %v, attendu ErrUnknownUser", err)
	}
}

// TestByIDRejectsMalformedID : la traduction vers le type uuid attrape
// la valeur avant le SQL, et le dit clairement.
func TestByIDRejectsMalformedID(t *testing.T) {
	t.Parallel()

	repo := newRepo(t)

	_, err := repo.ByID(t.Context(), identity.ID("pas-un-uuid"))
	if err == nil {
		t.Fatal("ByID() doit refuser un identifiant qui n'est pas un uuid")
	}
	if errors.Is(err, identity.ErrUnknownUser) {
		t.Error("un identifiant mal formé n'est pas un compte inconnu : c'est une erreur d'appel")
	}
}

// TestCreateRejectsTakenEmail vérifie la traduction du conflit d'unicité en
// erreur de domaine. C'est l'index unique qui tranche, pas une lecture préalable :
// deux créations simultanées ne peuvent donc pas passer toutes les deux.
func TestCreateRejectsTakenEmail(t *testing.T) {
	t.Parallel()

	repo := newRepo(t)
	first := testAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	if err := repo.Create(t.Context(), first); err != nil {
		t.Fatalf("Create() échoué : %v", err)
	}

	second := testAccount(t, "romain@exemple.fr", identity.RoleCollaborateur)
	if err := repo.Create(t.Context(), second); !errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("Create() = %v, attendu ErrEmailTaken", err)
	}
}

// TestTableConstraints exerce les garde-fous SQL. Ils doublent les
// validations du domaine, et c'est leur raison d'être : ils tiennent aussi face à
// un psql ouvert à la main ou à un futur chemin de code qui court-circuiterait
// identity. Le test vérifie qu'ils sont bien là, et qu'ils refusent.
func TestTableConstraints(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		corrupt func(*identity.User)
		want    string
	}{
		{
			name:    "email non normalisé",
			corrupt: func(u *identity.User) { u.Email = "Romain@Exemple.FR" },
			want:    "users_email_canonique",
		},
		{
			name:    "email avec des espaces autour",
			corrupt: func(u *identity.User) { u.Email = " romain@exemple.fr " },
			want:    "users_email_canonique",
		},
		{
			name:    "email vide",
			corrupt: func(u *identity.User) { u.Email = "" },
			want:    "users_email_canonique",
		},
		{
			name:    "nom d'affichage fait d'espaces",
			corrupt: func(u *identity.User) { u.DisplayName = "   " },
			want:    "users_nom_affichage_non_vide",
		},
		{
			name:    "empreinte vide",
			corrupt: func(u *identity.User) { u.PasswordHash = "" },
			want:    "users_empreinte_non_vide",
		},
		{
			name:    "rôle inconnu",
			corrupt: func(u *identity.User) { u.Role = identity.Role("administrateur") },
			want:    "users_role_connu",
		},
		{
			name:    "modification antérieure à la création",
			corrupt: func(u *identity.User) { u.UpdatedAt = u.CreatedAt.Add(-time.Hour) },
			want:    "users_horodatages_coherents",
		},
	}

	repo := newRepo(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := testAccount(t, "contrainte@exemple.fr", identity.RoleProprietaire)
			tc.corrupt(&user)

			err := repo.Create(t.Context(), user)
			if err == nil {
				t.Fatalf("Create() a accepté un compte que %s devait refuser", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Create() = %v, la contrainte %s était attendue", err, tc.want)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()

	repo := newRepo(t)
	original := testAccount(t, "romain@exemple.fr", identity.RoleCollaborateur)

	if err := repo.Create(t.Context(), original); err != nil {
		t.Fatalf("Create() échoué : %v", err)
	}

	modified := original
	modified.DisplayName = "Romain Badino"
	modified.PasswordHash = identity.PasswordHash("$argon2id$v=19$m=19456,t=2,p=1$bm91dmVhdXNlbA$bm91dmVsbGVlbXByZWludGU")
	modified.Role = identity.RoleProprietaire
	modified.Active = false
	modified.UpdatedAt = original.CreatedAt.Add(72 * time.Hour)

	if err := repo.Update(t.Context(), modified); err != nil {
		t.Fatalf("Update() échoué : %v", err)
	}

	reread, err := repo.ByID(t.Context(), original.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	compareAccounts(t, reread, modified)
}

// TestUpdateLeavesEmailAlone : l'email est l'identifiant de connexion, il
// n'est pas dans le SET. Le vérifier évite qu'un futur changement de requête
// l'y fasse entrer sans que personne ne l'ait décidé.
func TestUpdateLeavesEmailAlone(t *testing.T) {
	t.Parallel()

	repo := newRepo(t)
	original := testAccount(t, "romain@exemple.fr", identity.RoleProprietaire)

	if err := repo.Create(t.Context(), original); err != nil {
		t.Fatalf("Create() échoué : %v", err)
	}

	attempt := original
	attempt.Email = "autre@exemple.fr"
	attempt.CreatedAt = original.CreatedAt.Add(-24 * time.Hour)

	if err := repo.Update(t.Context(), attempt); err != nil {
		t.Fatalf("Update() échoué : %v", err)
	}

	reread, err := repo.ByID(t.Context(), original.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	if reread.Email != original.Email {
		t.Errorf("Email = %q, il ne devait pas changer depuis %q", reread.Email, original.Email)
	}
	if !reread.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt = %s, il ne devait pas changer depuis %s", reread.CreatedAt, original.CreatedAt)
	}
}

// TestUpdateUnknownAccount : une écriture dans le vide ne doit pas passer
// pour un succès.
func TestUpdateUnknownAccount(t *testing.T) {
	t.Parallel()

	repo := newRepo(t)
	ghost := testAccount(t, "fantome@exemple.fr", identity.RoleProprietaire)

	if err := repo.Update(t.Context(), ghost); !errors.Is(err, identity.ErrUnknownUser) {
		t.Errorf("Update() = %v, attendu ErrUnknownUser", err)
	}
}

func TestListSortsByEmail(t *testing.T) {
	t.Parallel()

	repo := newRepo(t)

	// Insérés dans un ordre qui n'est ni celui des emails ni celui des
	// identifiants, pour que le tri soit bien celui de la requête.
	for _, email := range []string{"zoe@exemple.fr", "amelie@exemple.fr", "romain@exemple.fr"} {
		if err := repo.Create(t.Context(), testAccount(t, email, identity.RoleCollaborateur)); err != nil {
			t.Fatalf("Create(%q) échoué : %v", email, err)
		}
	}

	accounts, err := repo.List(t.Context())
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

// TestListEmptyDatabase : aucune ligne n'est une réponse, pas une erreur.
func TestListEmptyDatabase(t *testing.T) {
	t.Parallel()

	accounts, err := newRepo(t).List(t.Context())
	if err != nil {
		t.Fatalf("List() échoué : %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("List() a rendu %d comptes sur une base vide", len(accounts))
	}
}

// TestRepoServiceEndToEnd branche le vrai dépôt sous le service du domaine,
// avec le vrai hacheur argon2id. C'est le seul test qui exerce la chaîne
// complète, et donc le seul qui prouve que le contrat du port est respecté par
// l'implémentation — pas seulement par la doublure des tests unitaires.
func TestRepoServiceEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("argon2id est lent par construction : test sauté en mode court")
	}
	t.Parallel()

	repo := newRepo(t)

	service, err := identity.NewAccountService(identity.ServiceOptions{
		Repo:   repo,
		Hasher: identity.NewArgon2idHasher(),
	})
	if err != nil {
		t.Fatalf("identity.NewAccountService() échoué : %v", err)
	}

	const password = "phrase de passe du chantier"

	created, err := service.Create(t.Context(), "  Romain@Exemple.FR ", "Romain Badino", password, identity.RoleProprietaire)
	if err != nil {
		t.Fatalf("Create() échoué : %v", err)
	}
	if created.Email != "romain@exemple.fr" {
		t.Fatalf("Email = %q, la normalisation du domaine n'a pas eu lieu", created.Email)
	}

	actor, err := service.Authenticate(t.Context(), "romain@exemple.fr", password)
	if err != nil {
		t.Fatalf("Authenticate() échoué : %v", err)
	}
	if actor.UserID() != created.ID {
		t.Errorf("UserID() = %q, attendu %q", actor.UserID(), created.ID)
	}
	if !actor.Allows(identity.ScopeMCP) {
		t.Error("le propriétaire doit détenir le scope mcp")
	}

	if _, err := service.Authenticate(t.Context(), "romain@exemple.fr", "un autre mot de passe"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("Authenticate() = %v, attendu ErrInvalidCredentials", err)
	}
	if _, err := service.Authenticate(t.Context(), "inconnu@exemple.fr", password); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("Authenticate() = %v, attendu ErrInvalidCredentials", err)
	}

	const updated = "une toute autre phrase de passe"
	if err := service.ResetPassword(t.Context(), created.ID, updated); err != nil {
		t.Fatalf("ResetPassword() échoué : %v", err)
	}
	if _, err := service.Authenticate(t.Context(), "romain@exemple.fr", updated); err != nil {
		t.Errorf("le nouveau mot de passe est refusé : %v", err)
	}

	if err := service.Deactivate(t.Context(), created.ID); err != nil {
		t.Fatalf("Deactivate() échoué : %v", err)
	}
	if _, err := service.Authenticate(t.Context(), "romain@exemple.fr", updated); !errors.Is(err, identity.ErrAccountDisabled) {
		t.Errorf("Authenticate() = %v, attendu ErrAccountDisabled", err)
	}
}
