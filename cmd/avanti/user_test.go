package main

import (
	"bytes"
	"strings"
	"testing"
)

// Les tests de `avanti user` portent sur ce qui se décide *avant* d'ouvrir la
// base : l'aiguillage des sous-commandes et la validation des arguments. C'est
// délibéré — le reste du chemin est exercé par les tests d'intégration du dépôt
// PostgreSQL et par ceux du domaine, qui n'ont pas besoin de passer par le
// parseur d'arguments pour être convaincants.
//
// Ces cas doivent donc tous échouer sans avoir touché à PostgreSQL, ce qui les
// rend exécutables partout, Docker ou non.

func TestUserRejectsMissingSubcommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{"user"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("« avanti user » sans sous-commande doit échouer")
	}
	if !strings.Contains(stderr.String(), "add") {
		t.Errorf("stderr = %q, doit rappeler les sous-commandes disponibles", stderr.String())
	}
}

func TestUserRejectsUnknownSubcommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{"user", "supprimer"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("une sous-commande inconnue doit échouer")
	}
	if !strings.Contains(err.Error(), "supprimer") {
		t.Errorf("erreur = %q, doit citer la sous-commande fautive", err.Error())
	}
}

func TestUserAddValidatesItsArguments(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "sans email",
			args: []string{"user", "add", "--nom", "Romain", "--role", "proprietaire", "--generate"},
			want: "--email",
		},
		{
			name: "sans nom",
			args: []string{"user", "add", "--email", "romain@exemple.fr", "--role", "proprietaire", "--generate"},
			want: "--nom",
		},
		{
			name: "sans rôle",
			args: []string{"user", "add", "--email", "romain@exemple.fr", "--nom", "Romain", "--generate"},
			want: "--role",
		},
		{
			name: "rôle inconnu",
			args: []string{
				"user", "add", "--email", "romain@exemple.fr", "--nom", "Romain",
				"--role", "administrateur", "--generate",
			},
			want: "administrateur",
		},
		{
			name: "drapeau inconnu",
			args: []string{
				"user", "add", "--email", "romain@exemple.fr", "--nom", "Romain",
				"--role", "proprietaire", "--admin",
			},
			want: "admin",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			err := run(t.Context(), tc.args, &stdout, &stderr)
			if err == nil {
				t.Fatal("user add doit refuser ces arguments")
			}
			if !strings.Contains(err.Error()+stderr.String(), tc.want) {
				t.Errorf("erreur = %q, stderr = %q — %q était attendu quelque part", err.Error(), stderr.String(), tc.want)
			}
		})
	}
}

// TestUserAddDoesNotPromptWithoutTerminal : la suite de tests n'a pas de
// terminal sur son entrée standard. La commande doit le dire clairement et
// renvoyer vers --generate, au lieu de lire une ligne en clair ou de bloquer.
func TestUserAddDoesNotPromptWithoutTerminal(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{
		"user", "add", "--email", "romain@exemple.fr", "--nom", "Romain", "--role", "proprietaire",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("user add doit échouer sans terminal ni --generate")
	}
	if !strings.Contains(err.Error(), "--generate") {
		t.Errorf("erreur = %q, doit orienter vers --generate", err.Error())
	}
}

func TestUserDisableAndEnableRequireAnEmail(t *testing.T) {
	t.Parallel()

	for _, subcmd := range []string{"disable", "enable"} {
		t.Run(subcmd, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			err := run(t.Context(), []string{"user", subcmd}, &stdout, &stderr)
			if err == nil {
				t.Fatalf("user %s doit exiger --email", subcmd)
			}
			if !strings.Contains(err.Error(), "--email") {
				t.Errorf("erreur = %q, doit nommer le drapeau manquant", err.Error())
			}
		})
	}
}

// TestUsageUserListsRoles : l'aide tire les rôles du domaine plutôt que de
// les recopier, de sorte qu'un rôle ajouté apparaisse sans qu'on y pense.
func TestUsageUserListsRoles(t *testing.T) {
	t.Parallel()

	var help bytes.Buffer
	usageUser(&help)

	for _, want := range []string{"proprietaire", "collaborateur", "--generate", "disable"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("l'aide de « user » ne mentionne pas %q", want)
		}
	}
}
