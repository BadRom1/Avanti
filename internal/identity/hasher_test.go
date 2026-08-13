package identity_test

import (
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// TestArgon2idHasher exerce l'implémentation réelle, celle que le reste des
// tests du domaine remplace par une doublure.
//
// Il est marqué lent : chaque hachage coûte volontairement des dizaines de
// millisecondes, et c'est bien ce qu'on veut vérifier ici. `go test -short`, que
// le hook de pre-commit utilise, le saute.
func TestArgon2idHasher(t *testing.T) {
	if testing.Short() {
		t.Skip("argon2id est lent par construction : test sauté en mode court")
	}
	t.Parallel()

	hasher := identity.NewArgon2idHasher()
	const password = "une phrase de passe de chantier"

	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash() échoué : %v", err)
	}

	// L'empreinte doit être une empreinte argon2id complète, paramètres inclus :
	// c'est ce qui permettra de relever le coût plus tard sans invalider ce qui
	// est déjà en base.
	brute := string(hash)
	if !strings.HasPrefix(brute, "$argon2id$v=19$") {
		t.Errorf("Hash() = %q, une empreinte argon2id est attendue", brute)
	}
	if !strings.Contains(brute, "m=19456,t=2,p=1") {
		t.Errorf("Hash() = %q, les paramètres attendus sont m=19456,t=2,p=1", brute)
	}
	if strings.Contains(brute, password) {
		t.Fatal("l'empreinte contient le mot de passe en clair")
	}

	matches, err := hasher.Verify(hash, password)
	if err != nil {
		t.Fatalf("Verify() échoué : %v", err)
	}
	if !matches {
		t.Error("Verify() refuse le mot de passe qui vient d'être haché")
	}

	for _, wrong := range []string{"", password + " ", strings.ToUpper(password), "autre chose"} {
		matches, err := hasher.Verify(hash, wrong)
		if err != nil {
			t.Errorf("Verify(%q) échoué : %v", wrong, err)
		}
		if matches {
			t.Errorf("Verify() accepte %q", wrong)
		}
	}
}

// TestArgon2idHasherSaltsEveryHash : deux comptes qui choisissent le même
// mot de passe ne doivent pas se reconnaître à leur empreinte.
func TestArgon2idHasherSaltsEveryHash(t *testing.T) {
	if testing.Short() {
		t.Skip("argon2id est lent par construction : test sauté en mode court")
	}
	t.Parallel()

	hasher := identity.NewArgon2idHasher()
	const password = "le meme mot de passe pour deux comptes"

	first, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash() échoué : %v", err)
	}
	second, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash() échoué : %v", err)
	}

	if first == second {
		t.Fatal("deux hachages du même mot de passe donnent la même empreinte : le sel n'est pas tiré")
	}

	// Chaque empreinte reste vérifiable, malgré des sels différents.
	for _, hash := range []identity.PasswordHash{first, second} {
		matches, err := hasher.Verify(hash, password)
		if err != nil || !matches {
			t.Errorf("Verify() = (%t, %v) sur une empreinte légitime", matches, err)
		}
	}
}

// TestArgon2idHasherRejectsUnreadableHash : une valeur qui n'est pas une
// empreinte doit produire une erreur, pas un refus silencieux — la différence
// compte, elle sépare une base corrompue d'un mot de passe erroné.
func TestArgon2idHasherRejectsUnreadableHash(t *testing.T) {
	t.Parallel()

	hasher := identity.NewArgon2idHasher()

	for _, hash := range []identity.PasswordHash{"", "pas une empreinte", "$argon2id$tronquée"} {
		matches, err := hasher.Verify(hash, "peu importe")
		if err == nil {
			t.Errorf("Verify(%q) n'a pas signalé d'empreinte illisible", string(hash))
		}
		if matches {
			t.Errorf("Verify(%q) a accepté le mot de passe", string(hash))
		}
	}
}
