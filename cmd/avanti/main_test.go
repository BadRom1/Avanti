package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRunPrintsBuildIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
	}{
		{name: "sous-commande", args: []string{"version"}},
		{name: "drapeau", args: []string{"--version"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			if err := run(t.Context(), tc.args, &stdout, &stderr); err != nil {
				t.Fatalf("run() a renvoyé une erreur inattendue : %v", err)
			}

			out := stdout.String()
			if !strings.HasPrefix(out, "avanti ") {
				t.Errorf("sortie = %q, doit commencer par le nom du binaire", out)
			}
			if !strings.HasSuffix(out, "\n") {
				t.Errorf("sortie = %q, doit finir par un retour à la ligne", out)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, doit rester vide sur un appel valide", stderr.String())
			}
		})
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{"--inconnu"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() doit échouer sur un drapeau inconnu")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, ne doit rien écrire quand les arguments sont invalides", stdout.String())
	}
}

// TestRunRejectsUnknownCommand vérifie qu'une faute de frappe sur la commande
// n'aboutit pas silencieusement au comportement par défaut, qui serait ici de
// démarrer un serveur.
func TestRunRejectsUnknownCommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{"servir"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() doit échouer sur une commande inconnue")
	}
	if !strings.Contains(err.Error(), "servir") {
		t.Errorf("erreur = %q, doit citer la commande fautive", err.Error())
	}
	if !strings.Contains(stderr.String(), "serve") {
		t.Errorf("stderr = %q, doit rappeler les commandes disponibles", stderr.String())
	}
}

func TestRunReportsWriteFailure(t *testing.T) {
	t.Parallel()

	err := run(t.Context(), []string{"version"}, failingWriter{}, io.Discard)
	if err == nil {
		t.Fatal("run() doit remonter une erreur d'écriture sur stdout")
	}
}

// TestServeRefusesInvalidConfiguration vérifie que la commande par défaut
// s'arrête sur une configuration invalide, avant d'ouvrir quoi que ce soit.
func TestServeRefusesInvalidConfiguration(t *testing.T) {
	// t.Setenv interdit le parallélisme : la commande serve lit l'environnement
	// réel du processus.
	t.Setenv("AVANTI_DATABASE_URL", "")

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("serve doit refuser de démarrer sans AVANTI_DATABASE_URL")
	}
	if !strings.Contains(err.Error(), "AVANTI_DATABASE_URL") {
		t.Errorf("erreur = %q, doit nommer la variable manquante", err.Error())
	}
}

// failingWriter simule une sortie standard fermée (pipe rompu).
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}
