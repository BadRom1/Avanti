package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRunPrintsBuildIdentity(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	if err := run([]string{"--version"}, &stdout, &stderr); err != nil {
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
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	err := run([]string{"--inconnu"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() doit échouer sur un drapeau inconnu")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, ne doit rien écrire quand les arguments sont invalides", stdout.String())
	}
}

func TestRunReportsWriteFailure(t *testing.T) {
	t.Parallel()

	err := run(nil, failingWriter{}, io.Discard)
	if err == nil {
		t.Fatal("run() doit remonter une erreur d'écriture sur stdout")
	}
}

// failingWriter simule une sortie standard fermée (pipe rompu).
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}
